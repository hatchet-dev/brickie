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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Prefix marks the file functions in a turn request.
const Prefix = "$fs."

// MaxFileBytes bounds one read or write; larger files go through download and upload.
const MaxFileBytes = 2 << 20

// Result is the output of a command run inside the sandbox.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Exec runs argv inside the sandbox with the workspace as the working directory.
type Exec func(ctx context.Context, argv []string) (Result, error)

type fsFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type fsInput struct {
	Path    string   `json:"path"`
	NewPath string   `json:"newPath"`
	URL     string   `json:"url"`
	Depth   int      `json:"depth"`
	Files   []fsFile `json:"files"`
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

func runFS(workspace, fn string, in fsInput) (out map[string]any, mutated bool, err error) {
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
		if info.Size() > MaxFileBytes {
			return nil, false, failf("too_large", "%q is %d bytes; read accepts up to %d, use files.upload for larger files", p, info.Size(), MaxFileBytes)
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
			b, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				return nil, false, failf("invalid_content", "content of %q is not base64", fp)
			}
			if total += len(b); total > MaxFileBytes {
				return nil, false, failf("too_large", "write accepts up to %d bytes per call, use files.download for larger files", MaxFileBytes)
			}
			if err := root.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
				return nil, len(entries) > 0, err
			}
			if err := root.WriteFile(fp, b, 0o644); err != nil {
				return nil, len(entries) > 0, err
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

func transfer(ctx context.Context, workspace, fn string, in fsInput, exec Exec) (map[string]any, bool, error) {
	p, err := localPath(in.Path)
	if err != nil {
		return nil, false, err
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, false, failf("invalid_url", "url must be an http or https URL")
	}
	args := []string{"curl", "-fsS", "--proto", "=http,https"}
	upload := fn == Prefix+"upload"
	if upload {
		args = append(args, "-T", p, in.URL)
	} else {
		args = append(args, "-L", "--create-dirs", "-o", p, in.URL)
	}
	res, err := exec(ctx, args)
	if err != nil {
		return nil, false, err
	}
	if res.ExitCode != 0 {
		return nil, !upload, failf("transfer_failed", "curl exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	if upload {
		return map[string]any{}, false, nil
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = root.Close() }()
	e, err := entryOf(root, p)
	if err != nil {
		return nil, true, err
	}
	return map[string]any{"entry": e}, true, nil
}

// Turn runs one file function; caller mistakes come back in the output as {"error": ...}.
func Turn(ctx context.Context, workspace, fn string, raw json.RawMessage, exec Exec) (out map[string]any, mutated bool, err error) {
	var in fsInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, false, err
	}
	switch fn {
	case Prefix + "download", Prefix + "upload":
		out, mutated, err = transfer(ctx, workspace, fn, in, exec)
	default:
		out, mutated, err = runFS(workspace, fn, in)
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
