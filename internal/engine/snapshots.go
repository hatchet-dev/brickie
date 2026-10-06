// Package engine defines the sandbox turn contract and the provider that runs it.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SnapshotStore interface {
	Read(id string) ([]byte, error)
	Write(id string, tar []byte) error
	Latest(sandboxID string) (string, error)
	SetLatest(sandboxID, snapshotID string) error
	Meta(sandboxID string) (SandboxMeta, error)
	SetMeta(meta SandboxMeta) error
	ListMeta() ([]SandboxMeta, error)
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
	return os.ReadFile(filepath.Join(s.Dir, id+".tar.gz")) // #nosec G304 -- id is a run id
}

func (s FSSnapshotStore) latestPath(sandboxID string) string {
	return filepath.Join(s.Dir, "latest", sandboxID)
}

// Latest returns the id of the newest snapshot of a sandbox.
func (s FSSnapshotStore) Latest(sandboxID string) (string, error) {
	b, err := os.ReadFile(s.latestPath(sandboxID)) // #nosec G304 -- sandboxID is validated by the provider
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// SetLatest records the newest snapshot of a sandbox.
func (s FSSnapshotStore) SetLatest(sandboxID, snapshotID string) error {
	if err := os.MkdirAll(filepath.Dir(s.latestPath(sandboxID)), 0o700); err != nil {
		return err
	}
	return s.writeAtomic(s.latestPath(sandboxID), []byte(snapshotID))
}

func (s FSSnapshotStore) metaPath(sandboxID string) string {
	return filepath.Join(s.Dir, "meta", sandboxID+".json")
}

// Meta returns what a sandbox was created with.
func (s FSSnapshotStore) Meta(sandboxID string) (SandboxMeta, error) {
	var m SandboxMeta
	b, err := os.ReadFile(s.metaPath(sandboxID)) // #nosec G304 -- sandboxID is validated by the provider
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// SetMeta records what a sandbox was created with.
func (s FSSnapshotStore) SetMeta(meta SandboxMeta) error {
	if err := os.MkdirAll(filepath.Dir(s.metaPath(meta.SandboxID)), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return s.writeAtomic(s.metaPath(meta.SandboxID), b)
}

// ListMeta returns every sandbox this store knows about.
func (s FSSnapshotStore) ListMeta() ([]SandboxMeta, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "meta"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	metas := []SandboxMeta{}
	for _, e := range entries {
		m, err := s.Meta(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			metas = append(metas, m)
		}
	}
	return metas, nil
}

// Prune removes snapshots, latest pointers, and metadata older than maxAge.
func (s FSSnapshotStore) Prune(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	for _, dir := range []string{s.Dir, filepath.Join(s.Dir, "latest"), filepath.Join(s.Dir, "meta")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || info.IsDir() || info.ModTime().After(cutoff) {
				continue
			}
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func (s FSSnapshotStore) Write(id string, tar []byte) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return s.writeAtomic(filepath.Join(s.Dir, id+".tar.gz"), tar)
}

func (s FSSnapshotStore) writeAtomic(dest string, b []byte) error {
	tmp := fmt.Sprintf("%s.tmp-%d", dest, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
