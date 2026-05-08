package billing

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// ObjectStore is a provider-agnostic interface for writing archive objects.
// Implementations cover AWS S3, minio, Cloudflare R2, and GCS S3-interop mode
// by pointing the S3-compatible client at the appropriate endpoint.
type ObjectStore interface {
	// Upload streams r to key. The implementation handles multipart upload
	// internally for large objects. sizeHint is -1 when unknown.
	// Returns the canonical URI (e.g. "s3://bucket/key") for the outbox event.
	Upload(ctx context.Context, key string, r io.Reader, sizeHint int64) (uri string, err error)

	// PutBytes writes a small object (manifest JSON, checksum file).
	PutBytes(ctx context.Context, key string, data []byte) error
}

// stubObjectStore captures uploaded objects in memory. Used by activity unit tests.
type stubObjectStore struct {
	mu       sync.Mutex
	objects  map[string][]byte
	bucket   string
	uploadFn func(key string) error
}

func newStubObjectStore(bucket string) *stubObjectStore {
	return &stubObjectStore{bucket: bucket, objects: map[string][]byte{}}
}

// SetUploadFn injects an error path triggered on Upload.
func (s *stubObjectStore) SetUploadFn(fn func(key string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploadFn = fn
}

func (s *stubObjectStore) Upload(_ context.Context, key string, r io.Reader, _ int64) (string, error) {
	s.mu.Lock()
	fn := s.uploadFn
	s.mu.Unlock()
	if fn != nil {
		if err := fn(key); err != nil {
			return "", err
		}
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.objects[key] = data
	s.mu.Unlock()
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

func (s *stubObjectStore) PutBytes(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), data...)
	return nil
}

// Get returns the stored bytes for key, or nil if absent.
func (s *stubObjectStore) Get(key string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[key]
}
