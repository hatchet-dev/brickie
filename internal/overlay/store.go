package overlay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Manifest struct {
	Layers []string `json:"layers"`
}

type Store struct {
	Root   string
	Remote ObjectStore
}

func blobKey(digest string) string {
	return "blobs/" + strings.ReplaceAll(digest, ":", "/")
}

func manifestKey(id string) string {
	return "snapshots/" + id + ".json"
}

func (s *Store) blobPath(digest string) string {
	return filepath.Join(s.Root, "blobs", strings.ReplaceAll(digest, ":", "-")+".tar.gz")
}

func (s *Store) LayerDir(digest string) string {
	return filepath.Join(s.Root, "layers", strings.ReplaceAll(digest, ":", "-"))
}

func (s *Store) manifestPath(id string) string {
	return filepath.Join(s.Root, "snapshots", id+".json")
}

func (s *Store) EnsureDirs() error {
	for _, dir := range []string{"blobs", "layers", "snapshots"} {
		if err := os.MkdirAll(filepath.Join(s.Root, dir), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// PutUpper freezes an upperdir as a content-addressed layer blob; Materialize
// extracts it on first use.
func (s *Store) PutUpper(upperDir string) (string, error) {
	tmp, err := os.CreateTemp(filepath.Join(s.Root, "blobs"), "layer-*.tar.gz")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	if out, err := exec.Command("tar", "--numeric-owner", "-czf", tmpPath, "-C", upperDir, ".").CombinedOutput(); err != nil {
		return "", fmt.Errorf("tar upper: %s", out)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))

	if err := os.Rename(tmpPath, s.blobPath(digest)); err != nil {
		return "", err
	}
	if s.Remote != nil {
		blob, err := os.Open(s.blobPath(digest))
		if err != nil {
			return "", err
		}
		err = s.Remote.Put(context.Background(), blobKey(digest), blob)
		blob.Close()
		if err != nil {
			return "", fmt.Errorf("offload layer %s: %w", digest, err)
		}
	}
	return digest, nil
}

// Materialize extracts a blob into layers/ if not already present.
// TODO: on cache miss, pull the blob from S3 before extracting.
func (s *Store) Materialize(digest string) error {
	dir := s.LayerDir(digest)
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	blob := s.blobPath(digest)
	if _, err := os.Stat(blob); err != nil {
		if s.Remote == nil {
			return fmt.Errorf("layer blob %s not found locally and no object store configured: %w", digest, err)
		}
		if err := s.fetchBlob(digest); err != nil {
			return err
		}
	}
	tmpDir := dir + ".tmp"
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	if out, err := exec.Command("tar", "--numeric-owner", "-xzf", blob, "-C", tmpDir).CombinedOutput(); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("extract layer %s: %s", digest, out)
	}
	return os.Rename(tmpDir, dir)
}

func (s *Store) fetchBlob(digest string) error {
	body, err := s.Remote.Get(context.Background(), blobKey(digest))
	if err != nil {
		return fmt.Errorf("fetch layer %s from object store: %w", digest, err)
	}
	defer body.Close()
	tmp := s.blobPath(digest) + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, s.blobPath(digest))
}

func (s *Store) WriteManifest(id string, layers []string) error {
	b, err := json.Marshal(Manifest{Layers: layers})
	if err != nil {
		return err
	}
	tmp := s.manifestPath(id) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.manifestPath(id)); err != nil {
		return err
	}
	if s.Remote != nil {
		if err := s.Remote.Put(context.Background(), manifestKey(id), strings.NewReader(string(b))); err != nil {
			return fmt.Errorf("offload manifest %s: %w", id, err)
		}
	}
	return nil
}

func (s *Store) ReadManifest(id string) ([]string, error) {
	b, err := os.ReadFile(s.manifestPath(id))
	if err != nil && s.Remote != nil {
		body, rerr := s.Remote.Get(context.Background(), manifestKey(id))
		if rerr != nil {
			return nil, fmt.Errorf("snapshot manifest %s: %w (remote: %v)", id, err, rerr)
		}
		defer body.Close()
		if b, err = io.ReadAll(body); err != nil {
			return nil, err
		}
		_ = os.WriteFile(s.manifestPath(id), b, 0o644)
	} else if err != nil {
		return nil, fmt.Errorf("snapshot manifest %s: %w", id, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m.Layers, nil
}
