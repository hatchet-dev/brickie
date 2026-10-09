// Package fsops serves the $fs.* file functions on a sandbox workspace that is
// a host directory.
package fsops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Prefix marks the file functions in a turn request.
const Prefix = "$fs."

// DefaultMaxFileBytes bounds one read or write unless the provider sets its own limit.
const DefaultMaxFileBytes = 2 << 20

// Staging carries files larger than the limit between the SDK and the
// workspace through an object store.
type Staging interface {
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, r io.Reader) error
	Delete(ctx context.Context, key string) error
	GetURL(ctx context.Context, key string) (string, error)
	NewKey(sandboxID string) string
}

// Options configures one Turn.
type Options struct {
	MaxBytes  int64
	Staging   Staging
	SandboxID string
}

type fsFile struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	StagedKey string `json:"stagedKey"`
}

type fsInput struct {
	Path      string   `json:"path"`
	NewPath   string   `json:"newPath"`
	URL       string   `json:"url"`
	Depth     int      `json:"depth"`
	Files     []fsFile `json:"files"`
	TimeoutMs int64    `json:"timeoutMs"`
}

type fsEntry struct {
	Name          string     `json:"name"`
	Path          string     `json:"path"`
	Type          string     `json:"type"`
	Size          int64      `json:"size"`
	Mode          uint32     `json:"mode"`
	Permissions   string     `json:"permissions"`
	ModifiedTime  *time.Time `json:"modifiedTime,omitempty"`
	SymlinkTarget string     `json:"symlinkTarget,omitempty"`
}
type fsFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f *fsFailure) Error() string { return f.Message }

func failf(code, format string, args ...any) *fsFailure {
	return &fsFailure{Code: code, Message: fmt.Sprintf(format, args...)}
}

func localPath(p string) (string, error) {
	p = filepath.Clean(p)
	if !filepath.IsLocal(p) {
		return "", failf("invalid_path", "path %q must be relative to the workspace and stay inside it", p)
	}
	return p, nil
}

func entryOf(root *os.Root, p string) (fsEntry, error) {
	info, err := root.Lstat(p)
	if err != nil {
		return fsEntry{}, err
	}
	mod := info.ModTime()
	e := fsEntry{
		Name:         filepath.Base(p),
		Path:         p,
		Type:         "file",
		Size:         info.Size(),
		Mode:         uint32(info.Mode().Perm()),
		Permissions:  info.Mode().Perm().String()[1:],
		ModifiedTime: &mod,
	}
	switch {
	case info.IsDir():
		e.Type = "dir"
	case info.Mode()&fs.ModeSymlink != 0:
		e.Type = "symlink"
		e.SymlinkTarget, _ = root.Readlink(p)
	}
	return e, nil
}

func listDir(root *os.Root, dir string, depth int) ([]fsEntry, error) {
	if depth < 1 {
		depth = 1
	}
	entries := []fsEntry{}
	err := fs.WalkDir(root.FS(), filepath.ToSlash(dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			if !d.IsDir() {
				return failf("not_a_directory", "%q is not a directory", dir)
			}
			return nil
		}
		e, err := entryOf(root, p)
		if err != nil {
			return err
		}
		entries = append(entries, e)
		rel := strings.TrimPrefix(strings.TrimPrefix(p, dir), "/")
		if d.IsDir() && strings.Count(rel, "/")+1 >= depth {
			return fs.SkipDir
		}
		return nil
	})
	return entries, err
}

func copyTree(root *os.Root, src, dst string) error {
	info, err := root.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := root.Readlink(src)
		if err != nil {
			return err
		}
		return root.Symlink(target, dst)
	case info.IsDir():
		if err := root.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		dir, err := root.Open(src)
		if err != nil {
			return err
		}
		names, err := dir.Readdirnames(-1)
		_ = dir.Close()
		if err != nil {
			return err
		}
		for _, name := range names {
			if err := copyTree(root, filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := root.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := root.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func runFS(ctx context.Context, workspace, fn string, in fsInput, opts Options) (out map[string]any, mutated bool, err error) {
	maxBytes := opts.MaxBytes
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = root.Close() }()

	p, err := localPath(in.Path)
	if err != nil && fn != Prefix+"write" {
		return nil, false, err
	}
	switch strings.TrimPrefix(fn, Prefix) {
	case "read":
		info, err := root.Stat(p)
		if err != nil {
			return nil, false, err
		}
		if info.IsDir() {
			return nil, false, failf("is_a_directory", "%q is a directory", p)
		}
		if info.Size() > maxBytes {
			if opts.Staging == nil {
				return nil, false, failf("too_large", "%q is %d bytes; read accepts up to %d (provider --max-file-bytes) and the provider has no --object-store for larger files", p, info.Size(), maxBytes)
			}
			f, err := root.Open(p)
			if err != nil {
				return nil, false, err
			}
			defer func() { _ = f.Close() }()
			key := opts.Staging.NewKey(opts.SandboxID)
			if err := opts.Staging.Put(ctx, key, f); err != nil {
				return nil, false, failf("staging_failed", "stage %q: %v", p, err)
			}
			url, err := opts.Staging.GetURL(ctx, key)
			if err != nil {
				return nil, false, failf("staging_failed", "sign %q: %v", p, err)
			}
			return map[string]any{"stagedKey": key, "url": url, "size": info.Size()}, false, nil
		}
		b, err := root.ReadFile(p)
		if err != nil {
			return nil, false, err
		}
		return map[string]any{"content": base64.StdEncoding.EncodeToString(b)}, false, nil
	case "write":
		entries := []fsEntry{}
		total := 0
		for _, f := range in.Files {
			fp, err := localPath(f.Path)
			if err != nil {
				return nil, false, err
			}
			if err := root.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
				return nil, len(entries) > 0, err
			}
			if f.StagedKey != "" {
				if opts.Staging == nil {
					return nil, len(entries) > 0, failf("staging_failed", "the provider has no --object-store")
				}
				if !strings.HasPrefix(f.StagedKey, "brickie-staging/"+opts.SandboxID+"/") {
					return nil, len(entries) > 0, failf("invalid", "staged key %q does not belong to this sandbox", f.StagedKey)
				}
				src, err := opts.Staging.Open(ctx, f.StagedKey)
				if err != nil {
					return nil, len(entries) > 0, failf("staging_failed", "open staged %q: %v", fp, err)
				}
				err = writeStream(root, fp, src)
				_ = src.Close()
				_ = opts.Staging.Delete(ctx, f.StagedKey)
				if err != nil {
					return nil, true, err
				}
			} else {
				b, err := base64.StdEncoding.DecodeString(f.Content)
				if err != nil {
					return nil, false, failf("invalid_content", "content of %q is not base64", fp)
				}
				if total += len(b); int64(total) > maxBytes {
					return nil, len(entries) > 0, failf("too_large", "write accepts up to %d bytes per call (provider --max-file-bytes); the SDK stages larger files through the provider's --object-store", maxBytes)
				}
				if err := root.WriteFile(fp, b, 0o644); err != nil {
					return nil, len(entries) > 0, err
				}
			}
			e, err := entryOf(root, fp)
			if err != nil {
				return nil, true, err
			}
			entries = append(entries, e)
		}
		return map[string]any{"entries": entries}, true, nil
	case "list":
		entries, err := listDir(root, p, in.Depth)
		if err != nil {
			return nil, false, err
		}
		return map[string]any{"entries": entries}, false, nil
	case "stat":
		e, err := entryOf(root, p)
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{"entry": nil}, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return map[string]any{"entry": e}, false, nil
	case "mkdir":
		if _, err := root.Stat(p); err == nil {
			return map[string]any{"created": false}, false, nil
		}
		if err := root.MkdirAll(p, 0o755); err != nil {
			return nil, false, err
		}
		return map[string]any{"created": true}, true, nil
	case "rename":
		np, err := localPath(in.NewPath)
		if err != nil {
			return nil, false, err
		}
		if err := root.Rename(p, np); err != nil {
			return nil, false, err
		}
		e, err := entryOf(root, np)
		if err != nil {
			return nil, true, err
		}
		return map[string]any{"entry": e}, true, nil
	case "copy":
		np, err := localPath(in.NewPath)
		if err != nil {
			return nil, false, err
		}
		if err := copyTree(root, p, np); err != nil {
			return nil, true, err
		}
		e, err := entryOf(root, np)
		if err != nil {
			return nil, true, err
		}
		return map[string]any{"entry": e}, true, nil
	case "remove":
		if p == "." {
			return nil, false, failf("invalid_path", "cannot remove the workspace root")
		}
		if err := root.RemoveAll(p); err != nil {
			return nil, false, err
		}
		return map[string]any{}, true, nil
	}
	return nil, false, fmt.Errorf("unknown sandbox fn %q", fn)
}

func writeStream(root *os.Root, p string, src io.Reader) error {
	tmp := p + ".brickie-tmp"
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		_ = root.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return root.Rename(tmp, p)
}

// safeClient refuses URLs that resolve to loopback, link-local, or private
// addresses, so a caller cannot use the provider to reach its own network.
var safeClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if ip.IP.IsLoopback() || ip.IP.IsPrivate() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() {
					return nil, fmt.Errorf("refusing to connect to %s (%s): private or local address", host, ip.IP)
				}
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("redirect to %s refused: https only", req.URL)
		}
		return nil
	},
}

func transfer(ctx context.Context, workspace, fn string, in fsInput) (map[string]any, bool, error) {
	p, err := localPath(in.Path)
	if err != nil {
		return nil, false, err
	}
	u, err := url.Parse(in.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, false, failf("invalid_url", "url must be an https URL")
	}
	if in.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(in.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = root.Close() }()
	if fn == Prefix+"upload" {
		f, err := root.Open(p)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil {
			return nil, false, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, in.URL, f)
		if err != nil {
			return nil, false, failf("invalid_url", "%v", err)
		}
		req.ContentLength = info.Size()
		req.Header.Set("Content-Type", "application/octet-stream")
		res, err := safeClient.Do(req)
		if err != nil {
			return nil, false, failf("transfer_failed", "upload %q: %v", p, err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		if res.StatusCode >= 300 {
			return nil, false, failf("transfer_failed", "upload %q: HTTP %d", p, res.StatusCode)
		}
		return map[string]any{}, false, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return nil, false, failf("invalid_url", "%v", err)
	}
	res, err := safeClient.Do(req)
	if err != nil {
		return nil, false, failf("transfer_failed", "download: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		return nil, false, failf("transfer_failed", "download: HTTP %d", res.StatusCode)
	}
	if err := root.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, false, err
	}
	if err := writeStream(root, p, res.Body); err != nil {
		return nil, true, failf("transfer_failed", "download into %q: %v", p, err)
	}
	e, err := entryOf(root, p)
	if err != nil {
		return nil, true, err
	}
	return map[string]any{"entry": e}, true, nil
}

// Turn runs one file function; caller mistakes come back in the output as {"error": ...}.
func Turn(ctx context.Context, workspace, fn string, raw json.RawMessage, opts Options) (out map[string]any, mutated bool, err error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxFileBytes
	}
	var in fsInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, false, err
	}
	switch fn {
	case Prefix + "download", Prefix + "upload":
		out, mutated, err = transfer(ctx, workspace, fn, in)
	default:
		out, mutated, err = runFS(ctx, workspace, fn, in, opts)
	}
	if err == nil {
		return out, mutated, nil
	}
	failure, ok := errors.AsType[*fsFailure](err)
	switch {
	case ok:
	case errors.Is(err, fs.ErrNotExist):
		failure = failf("not_found", "%v", err)
	case errors.Is(err, fs.ErrExist), errors.Is(err, fs.ErrInvalid), errors.Is(err, fs.ErrPermission):
		failure = failf("invalid", "%v", err)
	default:
		if _, isPath := errors.AsType[*fs.PathError](err); !isPath {
			return nil, mutated, err
		}
		failure = failf("invalid", "%v", err)
	}
	return map[string]any{"error": failure}, mutated, nil
}
