package engine

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
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

const stagingPrefix = "brickie-staging/"

const stagingURLExpiry = 15 * time.Minute

// Staging moves file bytes between the SDK and a workspace through an object
// store, so large files never pass through Hatchet and the sandbox needs no
// network. The SDK talks to the store with presigned URLs; the provider reads
// and writes it with its own credentials.
type Staging struct {
	bucket *blob.Bucket
	dir    string
}

// OpenStaging opens the store at url: s3://bucket?region=..., s3://bucket?endpoint=...&use_path_style=true
// for S3-compatible stores, gs://bucket, azblob://container, or file:///dir for
// one machine, where the SDK and the provider share the directory.
func OpenStaging(ctx context.Context, rawURL string) (*Staging, error) {
	s := &Staging{}
	if u, err := url.Parse(rawURL); err == nil && u.Scheme == "file" {
		s.dir = u.Path
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("metadata", "skip")
		u.RawQuery = q.Encode()
		rawURL = u.String()
	}
	bucket, err := blob.OpenBucket(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("open object store %q: %w", rawURL, err)
	}
	s.bucket = bucket
	return s, nil
}

// NewKey returns a fresh staging key for a sandbox.
func (s *Staging) NewKey(sandboxID string) string {
	return stagingPrefix + sandboxID + "/" + uuid.NewString()
}

// Presign returns a PUT and a GET URL for a key, both valid for stagingURLExpiry.
func (s *Staging) Presign(ctx context.Context, key string) (putURL, getURL string, err error) {
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

// GetURL returns a presigned GET URL for a key.
func (s *Staging) GetURL(ctx context.Context, key string) (string, error) {
	if s.dir != "" {
		return "file://" + filepath.Join(s.dir, key), nil
	}
	return s.bucket.SignedURL(ctx, key, &blob.SignedURLOptions{Method: "GET", Expiry: stagingURLExpiry})
}

// Open streams an object.
func (s *Staging) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.bucket.NewReader(ctx, key, nil)
}

// Put streams bytes into an object.
func (s *Staging) Put(ctx context.Context, key string, r io.Reader) error {
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

// Delete removes an object; a missing object is not an error.
func (s *Staging) Delete(ctx context.Context, key string) error {
	err := s.bucket.Delete(ctx, key)
	if err != nil && gcerrors.Code(err) != gcerrors.NotFound {
		return err
	}
	return nil
}

// Close releases the bucket connection.
func (s *Staging) Close() error {
	return s.bucket.Close()
}
