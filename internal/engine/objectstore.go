// Package engine defines the sandbox turn contract and the provider that runs it.
package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"gocloud.dev/blob"

	// Register the s3://, gs://, and azblob:// URL schemes with blob.OpenBucket.
	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/s3blob"
)

// ObjectSnapshotStore keeps turn snapshots in an object store so provider
// restarts and replicas share them; the local store is a read cache.
type ObjectSnapshotStore struct {
	Cache  FSSnapshotStore
	bucket *blob.Bucket
}

// NewObjectSnapshotStore opens a bucket by URL: s3://bucket/prefix (default
// AWS credential chain), s3://bucket?endpoint=host:9000&s3ForcePathStyle=true
// for S3-compatible stores, gs://bucket/prefix, or azblob://container/prefix.
func NewObjectSnapshotStore(ctx context.Context, url string, cache FSSnapshotStore) (*ObjectSnapshotStore, error) {
	bucket, err := blob.OpenBucket(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open object store %q: %w", url, err)
	}
	return &ObjectSnapshotStore{Cache: cache, bucket: bucket}, nil
}

func snapshotKey(id string) string {
	return "brickie-snapshots/" + id + ".tar.gz"
}

func (s *ObjectSnapshotStore) Read(id string) ([]byte, error) {
	if tar, err := s.Cache.Read(id); err == nil {
		return tar, nil
	}
	r, err := s.bucket.NewReader(context.Background(), snapshotKey(id), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch snapshot %s from object store: %w", id, err)
	}
	defer func() { _ = r.Close() }()
	tar, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	_ = s.Cache.Write(id, tar)
	return tar, nil
}

func (s *ObjectSnapshotStore) Write(id string, tar []byte) error {
	w, err := s.bucket.NewWriter(context.Background(), snapshotKey(id), nil)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, bytes.NewReader(tar)); err != nil {
		_ = w.Close()
		return fmt.Errorf("offload snapshot %s: %w", id, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("offload snapshot %s: %w", id, err)
	}
	return s.Cache.Write(id, tar)
}

// Close releases the bucket connection.
func (s *ObjectSnapshotStore) Close() error {
	return s.bucket.Close()
}
