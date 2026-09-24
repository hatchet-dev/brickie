package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(s.Dir, id+".tar.gz")
	tmp := fmt.Sprintf("%s.tmp-%d", dest, os.Getpid())
	if err := os.WriteFile(tmp, tar, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
