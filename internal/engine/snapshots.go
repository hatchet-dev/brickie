package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hatchet-dev/brickie/internal/overlay"
)

type SnapshotStore interface {
	Read(id string) ([]byte, error)
	Write(id string, tar []byte) error
}

type FSSnapshotStore struct {
	Dir string
}

func DefaultSnapshotDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "brickie", "snapshots")
	}
	return filepath.Join(home, ".brickie", "snapshots")
}

func (s FSSnapshotStore) Read(id string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.Dir, id+".tar.gz"))
}

// Prune deletes snapshot tars older than maxAge so local turns do not
// accumulate workspace archives forever.
func (s FSSnapshotStore) Prune(maxAge time.Duration) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(s.Dir, e.Name()))
		}
	}
}

func (s FSSnapshotStore) Write(id string, tar []byte) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	dest := filepath.Join(s.Dir, id+".tar.gz")
	tmp := fmt.Sprintf("%s.tmp-%d", dest, os.Getpid())
	if err := os.WriteFile(tmp, tar, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// ObjectSnapshotStore keeps turn snapshots in an object store, with the local
// filesystem store acting as a read cache.
type ObjectSnapshotStore struct {
	Cache  FSSnapshotStore
	Remote overlay.ObjectStore
}

func snapshotKey(id string) string {
	return "brickie-snapshots/" + id + ".tar.gz"
}

func (s ObjectSnapshotStore) Read(id string) ([]byte, error) {
	if tar, err := s.Cache.Read(id); err == nil {
		return tar, nil
	}
	body, err := s.Remote.Get(context.Background(), snapshotKey(id))
	if err != nil {
		return nil, fmt.Errorf("fetch snapshot %s from object store: %w", id, err)
	}
	defer body.Close()
	tar, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	_ = s.Cache.Write(id, tar)
	return tar, nil
}

func (s ObjectSnapshotStore) Write(id string, tar []byte) error {
	if err := s.Remote.Put(context.Background(), snapshotKey(id), bytes.NewReader(tar)); err != nil {
		return fmt.Errorf("offload snapshot %s: %w", id, err)
	}
	return s.Cache.Write(id, tar)
}
