package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	// Register the s3://, gs://, azblob://, and file:// URL schemes with blob.OpenBucket.
	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/s3blob"
)

const stagingURLExpiry = 15 * time.Minute

// Store keeps snapshots, latest-snapshot pointers, sandbox metadata, and
// staged large files in one bucket. The SDK reaches staged files with
// presigned URLs; the provider uses its own credentials.
type Store struct {
	bucket *blob.Bucket
	dir    string
}

// DefaultStoreURL is a directory under the home directory.
func DefaultStoreURL() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return "file://" + filepath.Join(home, ".brickie", "store")
}

// OpenStore opens s3://bucket?region=..., s3://bucket?endpoint=...&use_path_style=true
// for S3-compatible stores, gs://bucket, azblob://container, or file:///dir for
// one machine, where the SDK and the provider share the directory.
func OpenStore(ctx context.Context, rawURL string) (*Store, error) {
	s := &Store{}
	if u, err := url.Parse(rawURL); err == nil && u.Scheme == "file" {
		s.dir = u.Path
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("metadata", "skip")
		q.Set("no_tmp_dir", "true")
		u.RawQuery = q.Encode()
		rawURL = u.String()
	}
	bucket, err := blob.OpenBucket(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("open store %q: %w", rawURL, err)
	}
	s.bucket = bucket
	return s, nil
}

// Close releases the bucket.
func (s *Store) Close() error { return s.bucket.Close() }

// Local reports a file:// store, which only the provider's own machine can reach.
func (s *Store) Local() bool { return s.dir != "" }

// ReadSnapshot returns the workspace tar made by a turn.
func (s *Store) ReadSnapshot(ctx context.Context, id string) ([]byte, error) {
	return s.bucket.ReadAll(ctx, "snapshots/"+id+".tar.gz")
}

// WriteSnapshot stores a workspace tar under the turn id.
func (s *Store) WriteSnapshot(ctx context.Context, id string, tar []byte) error {
	return s.bucket.WriteAll(ctx, "snapshots/"+id+".tar.gz", tar, nil)
}

// Latest returns the id of the newest snapshot of a sandbox.
func (s *Store) Latest(ctx context.Context, sandboxID string) (string, error) {
	b, err := s.bucket.ReadAll(ctx, "latest/"+sandboxID)
	return strings.TrimSpace(string(b)), err
}

// SetLatest records the newest snapshot of a sandbox.
func (s *Store) SetLatest(ctx context.Context, sandboxID, snapshotID string) error {
	return s.bucket.WriteAll(ctx, "latest/"+sandboxID, []byte(snapshotID), nil)
}

// Meta returns what a sandbox was created with.
func (s *Store) Meta(ctx context.Context, sandboxID string) (SandboxMeta, error) {
	var m SandboxMeta
	b, err := s.bucket.ReadAll(ctx, "meta/"+sandboxID+".json")
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// SetMeta records what a sandbox was created with.
func (s *Store) SetMeta(ctx context.Context, meta SandboxMeta) error {
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return s.bucket.WriteAll(ctx, "meta/"+meta.SandboxID+".json", b, nil)
}

// ListMeta returns every sandbox this store knows.
func (s *Store) ListMeta(ctx context.Context) ([]SandboxMeta, error) {
	metas := []SandboxMeta{}
	iter := s.bucket.List(&blob.ListOptions{Prefix: "meta/"})
	for {
		obj, err := iter.Next(ctx)
		if err == io.EOF {
			return metas, nil
		}
		if err != nil {
			return nil, err
		}
		if m, err := s.Meta(ctx, strings.TrimSuffix(strings.TrimPrefix(obj.Key, "meta/"), ".json")); err == nil {
			metas = append(metas, m)
		}
	}
}

// Prune deletes everything older than maxAge.
func (s *Store) Prune(ctx context.Context, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	iter := s.bucket.List(nil)
	for {
		obj, err := iter.Next(ctx)
		if err != nil {
			return
		}
		if obj.ModTime.Before(cutoff) {
			_ = s.bucket.Delete(ctx, obj.Key)
		}
	}
}

// NewKey returns a fresh staging key owned by a sandbox.
func (s *Store) NewKey(sandboxID string) string {
	return "staging/" + sandboxID + "/" + uuid.NewString()
}

// Presign returns a PUT and a GET URL for a key, both valid for stagingURLExpiry.
func (s *Store) Presign(ctx context.Context, key string) (putURL, getURL string, err error) {
	if s.dir != "" {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(s.dir, key)), 0o700); err != nil {
			return "", "", err
		}
		u := "file://" + filepath.Join(s.dir, key)
		return u, u, nil
	}
	putURL, err = s.bucket.SignedURL(ctx, key, &blob.SignedURLOptions{Method: "PUT", Expiry: stagingURLExpiry, ContentType: "application/octet-stream"})
	if err != nil {
		return "", "", err
	}
	getURL, err = s.bucket.SignedURL(ctx, key, &blob.SignedURLOptions{Method: "GET", Expiry: stagingURLExpiry})
	return putURL, getURL, err
}

// GetURL returns a presigned GET URL for a staged file.
func (s *Store) GetURL(ctx context.Context, key string) (string, error) {
	if s.dir != "" {
		return "file://" + filepath.Join(s.dir, key), nil
	}
	return s.bucket.SignedURL(ctx, key, &blob.SignedURLOptions{Method: "GET", Expiry: stagingURLExpiry})
}

// Open streams a staged file.
func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.bucket.NewReader(ctx, key, nil)
}

// Put streams bytes into a staged file.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	w, err := s.bucket.NewWriter(ctx, key, nil)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// Delete removes a staged file; a missing one is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	err := s.bucket.Delete(ctx, key)
	if err != nil && gcerrors.Code(err) != gcerrors.NotFound {
		return err
	}
	return nil
}
