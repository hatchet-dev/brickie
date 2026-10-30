package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
	u, err := url.Parse(rawURL)
	if err == nil && u.Scheme != "file" && strings.Trim(u.Path, "/") != "" {
		return nil, fmt.Errorf("open store %q: a prefix goes in the query, not the path: %s://%s?prefix=%s/", rawURL, u.Scheme, u.Host, strings.Trim(u.Path, "/"))
	}
	if err == nil && u.Scheme == "file" {
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

func (s *Store) Close() error { return s.bucket.Close() }

// Local reports a file:// store, which only the provider's own machine can reach.
func (s *Store) Local() bool { return s.dir != "" }

// A snapshot id is <sandboxID>/<runID>. Bare run ids from before sandboxes
// owned their snapshots are refused, so a deleted sandbox cannot be restored
// from an object that deletion could not find.
var snapshotIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}/[0-9a-f-]+$`)

func (s *Store) OpenSnapshot(ctx context.Context, id string) (*blob.Reader, error) {
	if !snapshotIDRe.MatchString(id) {
		return nil, fmt.Errorf("invalid snapshot id %q", id)
	}
	return s.bucket.NewReader(ctx, "snapshots/"+id+".tar.gz", nil)
}

// NewSnapshotWriter streams a workspace tar into the store. Close commits it;
// cancelling ctx first abandons it.
func (s *Store) NewSnapshotWriter(ctx context.Context, id string) (*blob.Writer, error) {
	return s.bucket.NewWriter(ctx, "snapshots/"+id+".tar.gz", nil)
}

// IsNotFound reports a store read of something that does not exist, as
// opposed to a store that could not be read.
func IsNotFound(err error) bool { return gcerrors.Code(err) == gcerrors.NotFound }

func (s *Store) Latest(ctx context.Context, sandboxID string) (string, error) {
	b, err := s.bucket.ReadAll(ctx, "latest/"+sandboxID)
	return strings.TrimSpace(string(b)), err
}

func (s *Store) SetLatest(ctx context.Context, sandboxID, snapshotID string) error {
	return s.bucket.WriteAll(ctx, "latest/"+sandboxID, []byte(snapshotID), nil)
}

func (s *Store) Meta(ctx context.Context, sandboxID string) (SandboxMeta, error) {
	var m SandboxMeta
	b, err := s.bucket.ReadAll(ctx, "meta/"+sandboxID+".json")
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func (s *Store) SetMeta(ctx context.Context, meta SandboxMeta) error {
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return s.bucket.WriteAll(ctx, "meta/"+meta.SandboxID+".json", b, nil)
}

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

type storedResult struct {
	Result TurnResult `json:"result"`
	Prev   string     `json:"prev"`
}

// Result is the recorded outcome of a turn that stored a snapshot, with the
// latest pointer as it stood when the turn ran, so a retry of the same run
// publishes the snapshot instead of running the turn again, and only if no
// later turn has published since.
func (s *Store) Result(ctx context.Context, runID string) (result TurnResult, prev string, err error) {
	b, err := s.bucket.ReadAll(ctx, "results/"+runID+".json")
	if err != nil {
		return TurnResult{}, "", err
	}
	var r storedResult
	if err := json.Unmarshal(b, &r); err != nil {
		return TurnResult{}, "", err
	}
	return r.Result, r.Prev, nil
}

func (s *Store) SetResult(ctx context.Context, runID string, result TurnResult, prev string) error {
	b, err := json.Marshal(storedResult{Result: result, Prev: prev})
	if err != nil {
		return err
	}
	return s.bucket.WriteAll(ctx, "results/"+runID+".json", b, nil)
}

// DeleteSandbox removes everything the store holds for a sandbox: the
// metadata first, so a connect in the meantime finds no sandbox rather than an
// empty one, then the pointer, then the per-sandbox prefixes. Missing objects
// are not errors, so a retry completes a delete that failed part way.
func (s *Store) DeleteSandbox(ctx context.Context, sandboxID string) error {
	for _, key := range []string{"meta/" + sandboxID + ".json", "latest/" + sandboxID} {
		if err := s.bucket.Delete(ctx, key); err != nil && !IsNotFound(err) {
			return err
		}
	}
	for _, prefix := range []string{"snapshots/", "results/", "staging/"} {
		iter := s.bucket.List(&blob.ListOptions{Prefix: prefix + sandboxID + "/"})
		for {
			obj, err := iter.Next(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if err := s.bucket.Delete(ctx, obj.Key); err != nil && !IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// Prune deletes the provider's own objects older than maxAge and leaves the
// rest of the bucket alone. A sandbox whose latest pointer is younger than
// that keeps its metadata and the snapshot the pointer names, however old.
func (s *Store) Prune(ctx context.Context, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	keep := map[string]bool{}
	for _, prefix := range []string{"latest/", "snapshots/", "meta/", "results/", "staging/"} {
		iter := s.bucket.List(&blob.ListOptions{Prefix: prefix})
		for {
			obj, err := iter.Next(ctx)
			if err != nil {
				break
			}
			if keep[obj.Key] || !obj.ModTime.Before(cutoff) {
				if prefix == "latest/" {
					id := strings.TrimPrefix(obj.Key, "latest/")
					keep["meta/"+id+".json"] = true
					if snap, err := s.Latest(ctx, id); err == nil {
						keep["snapshots/"+snap+".tar.gz"] = true
					}
				}
				continue
			}
			_ = s.bucket.Delete(ctx, obj.Key)
		}
	}
}

func (s *Store) NewKey(sandboxID string) string {
	return "staging/" + sandboxID + "/" + rand.Text()
}

func (s *Store) Presign(ctx context.Context, key string) (putURL, getURL string, err error) {
	if s.dir != "" {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(s.dir, key)), 0o700); err != nil {
			return "", "", err
		}
		u := "file://" + filepath.Join(s.dir, key)
		return u, u, nil
	}
	putURL, err = s.bucket.SignedURL(ctx, key, &blob.SignedURLOptions{Method: "PUT", Expiry: stagingURLExpiry})
	if err != nil {
		return "", "", err
	}
	getURL, err = s.bucket.SignedURL(ctx, key, &blob.SignedURLOptions{Method: "GET", Expiry: stagingURLExpiry})
	return putURL, getURL, err
}

func (s *Store) GetURL(ctx context.Context, key string) (string, error) {
	if s.dir != "" {
		return "file://" + filepath.Join(s.dir, key), nil
	}
	return s.bucket.SignedURL(ctx, key, &blob.SignedURLOptions{Method: "GET", Expiry: stagingURLExpiry})
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.bucket.NewReader(ctx, key, nil)
}

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
