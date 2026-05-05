// Package compliance contains the ADR-017 contract test suites for CacheStore
// and RateLimiter. Both Redis and memory implementations must pass every case
// in these suites. Test files in each impl package call these exported helpers.
package compliance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
)

// CacheStoreFactory is a function that constructs a fresh, empty CacheStore
// for each sub-test. The returned cleanup function is called when the test ends.
type CacheStoreFactory func(t *testing.T) (store cache.CacheStore, cleanup func())

// RunCacheStoreCompliance runs the full CacheStore contract test suite against
// the store produced by factory. Call this from both the Redis and memory
// impl test files.
func RunCacheStoreCompliance(t *testing.T, factory CacheStoreFactory, advanceClock func(d time.Duration)) {
	t.Helper()

	t.Run("get_missing_returns_ErrNotFound", func(t *testing.T) {
		t.Parallel()
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		_, err := store.Get(ctx, "no-such-key")
		if err == nil {
			t.Fatal("expected ErrNotFound, got nil")
		}
		if err != cache.ErrNotFound {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("set_then_get_roundtrip", func(t *testing.T) {
		t.Parallel()
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		want := []byte("hello-gitscale")
		if err := store.Set(ctx, "k1", want, time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
		got, err := store.Get(ctx, "k1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got) != string(want) {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("ttl_expiry", func(t *testing.T) {
		// Not parallel: clock advance is shared state for memory impl.
		// Redis variant relies on advanceClock being nil and does a real short sleep.
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		if err := store.Set(ctx, "expiring", []byte("value"), 50*time.Millisecond); err != nil {
			t.Fatalf("Set: %v", err)
		}

		if advanceClock != nil {
			advanceClock(100 * time.Millisecond)
		} else {
			time.Sleep(200 * time.Millisecond)
		}

		_, err := store.Get(ctx, "expiring")
		if err != cache.ErrNotFound {
			t.Fatalf("expected ErrNotFound after TTL, got %v", err)
		}
	})

	t.Run("mget_mixed_present_absent", func(t *testing.T) {
		t.Parallel()
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		if err := store.Set(ctx, "present1", []byte("a"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := store.Set(ctx, "present2", []byte("b"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}

		keys := []string{"present1", "missing", "present2"}
		vals, err := store.MGet(ctx, keys)
		if err != nil {
			t.Fatalf("MGet: %v", err)
		}
		if len(vals) != 3 {
			t.Fatalf("expected 3 slots, got %d", len(vals))
		}
		if string(vals[0]) != "a" {
			t.Errorf("slot 0: got %q, want %q", vals[0], "a")
		}
		if vals[1] != nil {
			t.Errorf("slot 1 (missing key): expected nil, got %q", vals[1])
		}
		if string(vals[2]) != "b" {
			t.Errorf("slot 2: got %q, want %q", vals[2], "b")
		}
	})

	t.Run("delete_removes_key", func(t *testing.T) {
		t.Parallel()
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		if err := store.Set(ctx, "del-me", []byte("x"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := store.Delete(ctx, "del-me"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		_, err := store.Get(ctx, "del-me")
		if err != cache.ErrNotFound {
			t.Fatalf("expected ErrNotFound after Delete, got %v", err)
		}
	})

	t.Run("delete_missing_key_is_noop", func(t *testing.T) {
		t.Parallel()
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		if err := store.Delete(ctx, "never-existed"); err != nil {
			t.Fatalf("Delete on missing key returned error: %v", err)
		}
	})

	t.Run("cas_happy_path", func(t *testing.T) {
		t.Parallel()
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		initial := []byte("v1")
		if err := store.Set(ctx, "cas-key", initial, time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
		swapped, err := store.CompareAndSwap(ctx, "cas-key", initial, []byte("v2"), time.Minute)
		if err != nil {
			t.Fatalf("CAS: %v", err)
		}
		if !swapped {
			t.Fatal("expected swap=true, got false")
		}
		got, err := store.Get(ctx, "cas-key")
		if err != nil {
			t.Fatalf("Get after CAS: %v", err)
		}
		if string(got) != "v2" {
			t.Fatalf("got %q, want %q", got, "v2")
		}
	})

	t.Run("cas_mismatch_returns_false_value_unchanged", func(t *testing.T) {
		t.Parallel()
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		if err := store.Set(ctx, "cas-mismatch", []byte("actual"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
		swapped, err := store.CompareAndSwap(ctx, "cas-mismatch", []byte("wrong"), []byte("new"), time.Minute)
		if err != nil {
			t.Fatalf("CAS: %v", err)
		}
		if swapped {
			t.Fatal("expected swap=false on mismatch")
		}
		got, err := store.Get(ctx, "cas-mismatch")
		if err != nil {
			t.Fatalf("Get after failed CAS: %v", err)
		}
		if string(got) != "actual" {
			t.Fatalf("value changed: got %q, want %q", got, "actual")
		}
	})

	t.Run("cas_on_absent_key_with_empty_expected_succeeds", func(t *testing.T) {
		t.Parallel()
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		// expected="" means "key must be absent"; swap should succeed.
		swapped, err := store.CompareAndSwap(ctx, "cas-absent", []byte(""), []byte("first"), time.Minute)
		if err != nil {
			t.Fatalf("CAS: %v", err)
		}
		if !swapped {
			t.Fatal("expected swap=true on absent key with empty expected")
		}
		got, err := store.Get(ctx, "cas-absent")
		if err != nil {
			t.Fatalf("Get after CAS: %v", err)
		}
		if string(got) != "first" {
			t.Fatalf("got %q, want %q", got, "first")
		}
	})

	t.Run("concurrent_cas_exactly_one_wins_per_round", func(t *testing.T) {
		t.Parallel()
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		initial := []byte("start")
		if err := store.Set(ctx, "concurrent-cas", initial, time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}

		const goroutines = 20
		var (
			winsMu sync.Mutex
			wins   int64
		)
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				ok, err := store.CompareAndSwap(ctx, "concurrent-cas", initial, []byte("winner"), time.Minute)
				if err != nil {
					return
				}
				if ok {
					winsMu.Lock()
					wins++
					winsMu.Unlock()
				}
			}()
		}
		wg.Wait()

		winsMu.Lock()
		w := wins
		winsMu.Unlock()
		if w != 1 {
			t.Fatalf("expected exactly 1 CAS winner, got %d", w)
		}
	})

	t.Run("ping_succeeds", func(t *testing.T) {
		t.Parallel()
		store, cleanup := factory(t)
		defer cleanup()

		if err := store.Ping(context.Background()); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})
}
