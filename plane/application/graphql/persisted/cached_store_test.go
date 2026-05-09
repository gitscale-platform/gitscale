package persisted_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/persisted"
	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/google/uuid"
)

type stubInner struct {
	gets  int64
	puts  int64
	body  map[string]string
	getErr error
}

func newStubInner() *stubInner { return &stubInner{body: map[string]string{}} }

func (s *stubInner) Get(_ context.Context, hash string) (string, error) {
	atomic.AddInt64(&s.gets, 1)
	if s.getErr != nil {
		return "", s.getErr
	}
	q, ok := s.body[hash]
	if !ok {
		return "", persisted.ErrNotFound
	}
	return q, nil
}

func (s *stubInner) Put(_ context.Context, hash, query string, _ uuid.UUID) error {
	atomic.AddInt64(&s.puts, 1)
	if existing, ok := s.body[hash]; ok && existing != query {
		return persisted.ErrHashConflict
	}
	s.body[hash] = query
	return nil
}

func TestCachedStore_GetMissThenHit(t *testing.T) {
	t.Parallel()
	inner := newStubInner()
	hash := persisted.HashFor("{ a }")
	_ = inner.Put(context.Background(), hash, "{ a }", uuid.New())

	cs := persisted.NewCachedStore(inner, cache.NewMemoryStore(nil))

	got, err := cs.Get(context.Background(), hash)
	if err != nil || got != "{ a }" {
		t.Fatalf("first get: %q %v", got, err)
	}
	if atomic.LoadInt64(&inner.gets) != 1 {
		t.Errorf("inner gets after miss: %d, want 1", inner.gets)
	}

	// Second call should hit cache.
	if _, err := cs.Get(context.Background(), hash); err != nil {
		t.Fatalf("second get: %v", err)
	}
	if atomic.LoadInt64(&inner.gets) != 1 {
		t.Errorf("inner gets after hit: %d, want 1", inner.gets)
	}
}

func TestCachedStore_GetNotFoundPropagates(t *testing.T) {
	t.Parallel()
	cs := persisted.NewCachedStore(newStubInner(), cache.NewMemoryStore(nil))
	_, err := cs.Get(context.Background(), "sha256:absent")
	if !errors.Is(err, persisted.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestCachedStore_PutPrimesCache(t *testing.T) {
	t.Parallel()
	inner := newStubInner()
	cs := persisted.NewCachedStore(inner, cache.NewMemoryStore(nil))

	hash := persisted.HashFor("{ b }")
	if err := cs.Put(context.Background(), hash, "{ b }", uuid.New()); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Subsequent Get should not hit inner.
	if _, err := cs.Get(context.Background(), hash); err != nil {
		t.Fatalf("get after put: %v", err)
	}
	if atomic.LoadInt64(&inner.gets) != 0 {
		t.Errorf("inner gets after put-prime: %d, want 0", inner.gets)
	}
}

func TestCachedStore_NilCacheFallsThrough(t *testing.T) {
	t.Parallel()
	inner := newStubInner()
	hash := persisted.HashFor("{ c }")
	_ = inner.Put(context.Background(), hash, "{ c }", uuid.New())

	cs := persisted.NewCachedStore(inner, nil)
	got, err := cs.Get(context.Background(), hash)
	if err != nil || got != "{ c }" {
		t.Fatalf("nil cache: %q %v", got, err)
	}
}

func TestHashFor_StableHexEncoding(t *testing.T) {
	t.Parallel()
	got := persisted.HashFor("{ a }")
	if len(got) != len("sha256:")+64 {
		t.Errorf("hash length: %d", len(got))
	}
	if got != persisted.HashFor("{ a }") {
		t.Error("hash not deterministic")
	}
}

// TestCachedStore_RespectsCustomTTL ensures we don't accidentally hard-code
// DefaultTTL into the SET path.
func TestCachedStore_RespectsCustomTTL(t *testing.T) {
	t.Parallel()
	inner := newStubInner()
	hash := persisted.HashFor("{ d }")
	_ = inner.Put(context.Background(), hash, "{ d }", uuid.New())

	cs := persisted.NewCachedStore(inner, cache.NewMemoryStore(nil))
	cs.TTL = 50 * time.Millisecond

	if _, err := cs.Get(context.Background(), hash); err != nil {
		t.Fatalf("get: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	// After TTL, second Get must miss cache (inner.gets goes from 1 → 2).
	if _, err := cs.Get(context.Background(), hash); err != nil {
		t.Fatalf("get-after-ttl: %v", err)
	}
	if atomic.LoadInt64(&inner.gets) < 2 {
		t.Errorf("expected cache miss after TTL; inner gets=%d", inner.gets)
	}
}
