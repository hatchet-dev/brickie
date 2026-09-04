package overlay

import (
	"context"
	"fmt"
	"io"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/s3blob"
)

// ObjectStore offloads frozen layers and snapshot manifests so any node can
// restore a workspace: blobs/sha256/<digest> and snapshots/<id>.json.
type ObjectStore interface {
	Put(ctx context.Context, key string, body io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

type BucketStore struct {
	bucket *blob.Bucket
}

// NewBucketStore opens an object store by URL:
//
//	s3://bucket/prefix                                   AWS S3 (default credential chain, IRSA included)
//	s3://bucket?endpoint=minio.local:9000&s3ForcePathStyle=true   any S3-compatible store
//	gs://bucket/prefix                                   Google Cloud Storage
//	azblob://container/prefix                            Azure Blob Storage
func NewBucketStore(ctx context.Context, url string) (*BucketStore, error) {
	bucket, err := blob.OpenBucket(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open object store %q: %w", url, err)
	}
	return &BucketStore{bucket: bucket}, nil
}

func (b *BucketStore) Put(ctx context.Context, key string, body io.Reader) error {
	w, err := b.bucket.NewWriter(ctx, key, nil)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, body); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func (b *BucketStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.bucket.NewReader(ctx, key, nil)
}

func (b *BucketStore) Close() error {
	return b.bucket.Close()
}
