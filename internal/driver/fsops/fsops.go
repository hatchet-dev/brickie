// Package fsops serves the $fs.* file functions on a sandbox workspace through
// the sandboxd REST API, so the provider never touches sandbox files itself.
package fsops

import (
	"bytes"
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
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/hatchet-dev/brickie/internal/sandboxd"
)

// Prefix marks the file functions in a turn request.
const Prefix = "$fs."

// DefaultMaxFileBytes bounds one read or write unless the provider sets its own limit.
const DefaultMaxFileBytes = 2 << 20

// Marker is the file a driver may keep in the workspace root to recognise a
// pod it has already bound; listings and snapshots leave it out.
const Marker = ".brickie-sandbox"

// Staging carries files larger than the limit between the SDK and the
// workspace through the object store.
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
	Name         string     `json:"name"`
	Path         string     `json:"path"`
	Type         string     `json:"type"`
	Size         int64      `json:"size"`
	Mode         uint32     `json:"mode"`
	Permissions  string     `json:"permissions"`
	ModifiedTime *time.Time `json:"modifiedTime,omitempty"`
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
	p = path.Clean(p)
	if p != "." && !fs.ValidPath(p) {
		return "", failf("invalid_path", "path %q must be relative to the workspace and stay inside it", p)
	}
	return p, nil
}

func toEntry(dir string, e sandboxd.Entry) fsEntry {
	mode, _ := strconv.ParseUint(e.Mode, 8, 32)
	out := fsEntry{
		Name:        e.Name,
		Path:        path.Join(dir, e.Name),
		Type:        "file",
		Size:        e.Size,
		Mode:        uint32(mode),
		Permissions: fs.FileMode(mode).Perm().String()[1:],
	}
	if e.Type == "directory" {
		out.Type = "dir"
	}
	if t, err := time.Parse(time.RFC3339, e.ModifiedAt); err == nil {
		out.ModifiedTime = &t
	}
	return out
}

func stat(ctx context.Context, c *sandboxd.Client, p string) (fsEntry, error) {
	e, err := c.Stat(ctx, p)
	if err != nil {
		return fsEntry{}, err
	}
	return toEntry(path.Dir(p), e), nil
}

func listDir(ctx context.Context, c *sandboxd.Client, dir string, depth int) ([]fsEntry, error) {
	raw, err := c.List(ctx, dir)
	if errors.Is(err, sandboxd.ErrNotDirectory) {
		return nil, failf("not_a_directory", "%q is not a directory", dir)
	}
	if err != nil {
		return nil, err
	}
	entries := []fsEntry{}
	for _, r := range raw {
		if dir == "." && r.Name == Marker {
			continue
		}
		e := toEntry(dir, r)
		entries = append(entries, e)
		if e.Type == "dir" && depth > 1 {
			sub, err := listDir(ctx, c, e.Path, depth-1)
			if err != nil {
				return nil, err
			}
			entries = append(entries, sub...)
		}
	}
	return entries, nil
}

func mustRun(ctx context.Context, c *sandboxd.Client, argv ...string) error {
	res, err := c.Run(ctx, argv, 0)
	if err != nil {
		return err
	}
	if res.ExitCode == escapeExit {
		return failf("invalid_path", "path resolves outside the workspace")
	}
	if res.ExitCode != 0 {
		return failf("invalid", "%s", strings.TrimSpace(res.Stderr))
	}
	return nil
}

const escapeExit = 86

// confine runs a command only after the nearest existing ancestor of a path
// resolves, through any symlinks, to a place under the workspace. The check
// and the command run inside the sandbox; only the agent itself could race it.
const confine = `t=$1; shift
p=$t
while [ ! -e "$p" ]; do
  case $p in .|/) break;; esac
  p=$(dirname -- "$p")
done
r=$(readlink -f -- "$p") || exit 86
case $r in /workspace|/workspace/*) ;; *) exit 86;; esac
exec "$@"`

func confined(target string, argv ...string) []string {
	return append([]string{"sh", "-c", confine, "sh", target}, argv...)
}

func runFS(ctx context.Context, c *sandboxd.Client, fn string, in fsInput, opts Options) (out map[string]any, mutated bool, err error) {
	maxBytes := opts.MaxBytes
	p, err := localPath(in.Path)
	if err != nil && fn != Prefix+"write" {
		return nil, false, err
	}
	switch strings.TrimPrefix(fn, Prefix) {
	case "read":
		e, err := stat(ctx, c, p)
		if err != nil {
			return nil, false, err
		}
		if e.Type == "dir" {
			return nil, false, failf("is_a_directory", "%q is a directory", p)
		}
		body, err := c.Open(ctx, p)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = body.Close() }()
		if e.Size > maxBytes {
			if opts.Staging == nil {
				return nil, false, failf("too_large", "%q is %d bytes; read accepts up to %d (provider --max-file-bytes) and the provider store is not an object store", p, e.Size, maxBytes)
			}
			key := opts.Staging.NewKey(opts.SandboxID)
			if err := opts.Staging.Put(ctx, key, body); err != nil {
				return nil, false, failf("staging_failed", "stage %q: %v", p, err)
			}
			url, err := opts.Staging.GetURL(ctx, key)
			if err != nil {
				return nil, false, failf("staging_failed", "sign %q: %v", p, err)
			}
			return map[string]any{"stagedKey": key, "url": url, "size": e.Size}, false, nil
		}
		b, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
		if err != nil {
			return nil, false, err
		}
		if int64(len(b)) > maxBytes {
			return nil, false, failf("too_large", "%q grew past %d bytes while being read; read it again", p, maxBytes)
		}
		return map[string]any{"content": base64.StdEncoding.EncodeToString(b)}, false, nil
	case "write":
		entries := []fsEntry{}
		total := 0
		for _, f := range in.Files {
			fp, err := localPath(f.Path)
			if err != nil {
				return nil, len(entries) > 0, err
			}
			if f.StagedKey != "" {
				if opts.Staging == nil {
					return nil, len(entries) > 0, failf("staging_failed", "the provider store is not an object store")
				}
				if !strings.HasPrefix(f.StagedKey, "staging/"+opts.SandboxID+"/") {
					return nil, len(entries) > 0, failf("invalid", "staged key %q does not belong to this sandbox", f.StagedKey)
				}
				src, err := opts.Staging.Open(ctx, f.StagedKey)
				if err != nil {
					return nil, len(entries) > 0, failf("staging_failed", "open staged %q: %v", fp, err)
				}
				err = c.Write(ctx, fp, src, -1)
				_ = src.Close()
				if err != nil {
					return nil, true, err
				}
			} else {
				b, err := base64.StdEncoding.DecodeString(f.Content)
				if err != nil {
					return nil, len(entries) > 0, failf("invalid_content", "content of %q is not base64", fp)
				}
				if total += len(b); int64(total) > maxBytes {
					return nil, len(entries) > 0, failf("too_large", "write accepts up to %d bytes per call (provider --max-file-bytes); the SDK stages larger files through the provider's object store", maxBytes)
				}
				if err := c.Write(ctx, fp, bytes.NewReader(b), int64(len(b))); err != nil {
					return nil, len(entries) > 0, err
				}
			}
			e, err := stat(ctx, c, fp)
			if err != nil {
				return nil, true, err
			}
			entries = append(entries, e)
		}
		return map[string]any{"entries": entries}, true, nil
	case "list":
		entries, err := listDir(ctx, c, p, max(in.Depth, 1))
		if err != nil {
			return nil, false, err
		}
		return map[string]any{"entries": entries}, false, nil
	case "stat":
		e, err := stat(ctx, c, p)
		if sandboxd.IsNotFound(err) {
			return map[string]any{"entry": nil}, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return map[string]any{"entry": e}, false, nil
	case "mkdir":
		if _, err := c.Stat(ctx, p); err == nil {
			return map[string]any{"created": false}, false, nil
		}
		if err := mustRun(ctx, c, confined(p, "mkdir", "-p", "--", p)...); err != nil {
			return nil, false, err
		}
		return map[string]any{"created": true}, true, nil
	case "rename", "copy":
		np, err := localPath(in.NewPath)
		if err != nil {
			return nil, false, err
		}
		if _, err := c.Stat(ctx, p); err != nil {
			return nil, false, err
		}
		argv := confined(path.Dir(p), confined(np, "mv", "-T", "--", p, np)...)
		if fn == Prefix+"copy" {
			argv = confined(path.Dir(p), confined(np, "cp", "-aT", "--", p, np)...)
		}
		if err := mustRun(ctx, c, confined(path.Dir(np), "mkdir", "-p", "--", path.Dir(np))...); err != nil {
			return nil, false, err
		}
		if err := mustRun(ctx, c, argv...); err != nil {
			return nil, true, err
		}
		e, err := stat(ctx, c, np)
		if err != nil {
			return nil, true, err
		}
		return map[string]any{"entry": e}, true, nil
	case "remove":
		if p == "." {
			return nil, false, failf("invalid_path", "cannot remove the workspace root")
		}
		if err := mustRun(ctx, c, confined(path.Dir(p), "rm", "-rf", "--", p)...); err != nil {
			return nil, false, err
		}
		return map[string]any{}, true, nil
	}
	return nil, false, fmt.Errorf("unknown sandbox fn %q", fn)
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

func transfer(ctx context.Context, c *sandboxd.Client, fn string, in fsInput) (map[string]any, bool, error) {
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
	if fn == Prefix+"upload" {
		e, err := stat(ctx, c, p)
		if err != nil {
			return nil, false, err
		}
		body, err := c.Open(ctx, p)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = body.Close() }()
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, in.URL, body)
		if err != nil {
			return nil, false, failf("invalid_url", "%v", err)
		}
		req.ContentLength = e.Size
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
	if err := c.Write(ctx, p, res.Body, res.ContentLength); err != nil {
		return nil, true, failf("transfer_failed", "download into %q: %v", p, err)
	}
	e, err := stat(ctx, c, p)
	if err != nil {
		return nil, true, err
	}
	return map[string]any{"entry": e}, true, nil
}

// Turn runs one file function; caller mistakes come back in the output as {"error": ...}.
func Turn(ctx context.Context, c *sandboxd.Client, fn string, raw json.RawMessage, opts Options) (out map[string]any, mutated bool, err error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxFileBytes
	}
	var in fsInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, false, err
	}
	switch fn {
	case Prefix + "download", Prefix + "upload":
		out, mutated, err = transfer(ctx, c, fn, in)
	default:
		out, mutated, err = runFS(ctx, c, fn, in, opts)
	}
	if err == nil {
		return out, mutated, nil
	}
	failure, ok := errors.AsType[*fsFailure](err)
	switch {
	case ok:
	case sandboxd.IsNotFound(err):
		failure = failf("not_found", "%v", err)
	default:
		if _, isREST := errors.AsType[*sandboxd.Error](err); !isREST {
			return nil, mutated, err
		}
		failure = failf("invalid", "%v", err)
	}
	return map[string]any{"error": failure}, mutated, nil
}
