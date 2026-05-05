package compliance

import (
	"context"
	"sync"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
)

// RateLimiterFactory constructs a fresh RateLimiter for each sub-test.
// advanceClock may be nil for the Redis impl (uses real time).
type RateLimiterFactory func(t *testing.T) (limiter ratelimit.RateLimiter, advanceClock func(seconds float64), cleanup func())

// RunRateLimiterCompliance runs the full RateLimiter contract test suite.
// Call this from both the Redis and memory impl test files.
func RunRateLimiterCompliance(t *testing.T, factory RateLimiterFactory) {
	t.Helper()

	t.Run("first_take_granted_full_bucket", func(t *testing.T) {
		t.Parallel()
		lim, _, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		granted, remaining, err := lim.Take(ctx, "bucket-a", 10, 1, 1)
		if err != nil {
			t.Fatalf("Take: %v", err)
		}
		if !granted {
			t.Fatal("expected granted=true on first Take")
		}
		// remaining should be capacity - n = 9 (±tiny float rounding)
		if remaining < 8.9 || remaining > 9.1 {
			t.Fatalf("expected remaining≈9, got %f", remaining)
		}
	})

	t.Run("exhausted_bucket_denies", func(t *testing.T) {
		t.Parallel()
		lim, _, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		// Drain the 5-token bucket in one shot; refillPerSec=0 so no tokens
		// accumulate between calls even under real-time Redis round-trip latency.
		granted, _, err := lim.Take(ctx, "exhaust-bucket", 5, 0, 5)
		if err != nil {
			t.Fatalf("Take (drain): %v", err)
		}
		if !granted {
			t.Fatal("expected first drain to be granted")
		}

		// Next take should be denied.
		granted, remaining, err := lim.Take(ctx, "exhaust-bucket", 5, 0, 1)
		if err != nil {
			t.Fatalf("Take (denied): %v", err)
		}
		if granted {
			t.Fatal("expected denied after exhaustion")
		}
		if remaining > 0.01 {
			t.Fatalf("expected remaining≈0, got %f", remaining)
		}
	})

	t.Run("refill_restores_tokens", func(t *testing.T) {
		// Not parallel: advanceClock is shared state for memory.
		lim, advanceClock, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		// Drain completely.
		_, _, err := lim.Take(ctx, "refill-bucket", 2, 2, 2)
		if err != nil {
			t.Fatalf("Take (drain): %v", err)
		}

		// Advance 1 second → +2 tokens refilled (back to capacity 2).
		if advanceClock != nil {
			advanceClock(1.0)
		} else {
			// Redis variant: real wait; use a small bucket + fast refill.
			// The factory should configure accordingly. Skip if no clock control.
			t.Skip("no clock control for Redis; skip refill test")
		}

		granted, _, err := lim.Take(ctx, "refill-bucket", 2, 2, 1)
		if err != nil {
			t.Fatalf("Take (after refill): %v", err)
		}
		if !granted {
			t.Fatal("expected granted after refill")
		}
	})

	t.Run("refill_never_exceeds_capacity", func(t *testing.T) {
		t.Parallel()
		lim, advanceClock, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		if advanceClock == nil {
			t.Skip("no clock control; skip capacity ceiling test")
		}

		// Advance far beyond what refill can fill.
		advanceClock(1000.0)

		granted, remaining, err := lim.Take(ctx, "ceiling-bucket", 5, 1, 1)
		if err != nil {
			t.Fatalf("Take: %v", err)
		}
		if !granted {
			t.Fatal("expected granted")
		}
		// Remaining after taking 1 from a full 5-capacity bucket = 4.
		if remaining > 4.01 {
			t.Fatalf("remaining %f exceeds capacity-1=4; ceiling not enforced", remaining)
		}
	})

	t.Run("concurrent_takes_total_granted_equals_floor", func(t *testing.T) {
		t.Parallel()
		lim, _, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		const capacity = 10.0
		const goroutines = 30
		var (
			grantedMu sync.Mutex
			granted   int64
		)
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				ok, _, err := lim.Take(ctx, "concurrent-bucket", capacity, 0, 1)
				if err != nil {
					return
				}
				if ok {
					grantedMu.Lock()
					granted++
					grantedMu.Unlock()
				}
			}()
		}
		wg.Wait()

		// With refillPerSec=0, no refill happens. Exactly capacity=10 grants expected.
		grantedMu.Lock()
		g := granted
		grantedMu.Unlock()
		if g != int64(capacity) {
			t.Fatalf("expected %d grants, got %d", int64(capacity), g)
		}
	})
}
