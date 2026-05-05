package cache_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/compliance"
	redistc "github.com/testcontainers/testcontainers-go/modules/redis"
)

// --- Memory impl ---

func TestCacheStore_Memory_Compliance(t *testing.T) {
	// Each sub-test gets its own FakeClock via the factory. The advanceClock
	// function below is only safe because the TTL expiry sub-test is NOT
	// parallel (see compliance.RunCacheStoreCompliance). All other sub-tests
	// create their own store+clock in factory and do not share state.
	var clkRef atomic.Pointer[cache.FakeClock]

	factory := func(t *testing.T) (cache.CacheStore, func()) {
		t.Helper()
		clk := cache.NewFakeClock(time.Now())
		clkRef.Store(clk)
		return cache.NewMemoryStore(clk), func() {}
	}
	advance := func(d time.Duration) {
		if c := clkRef.Load(); c != nil {
			c.Advance(d)
		}
	}
	compliance.RunCacheStoreCompliance(t, factory, advance)
}

// --- Redis impl ---

func TestCacheStore_Redis_Compliance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Redis integration test in -short mode")
	}

	ctx := context.Background()
	container, err := redistc.Run(ctx, "redis:7")
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminate redis container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}

	// Atomic counter ensures unique namespace per sub-test even from parallel goroutines.
	var counter atomic.Int64

	factory := func(t *testing.T) (cache.CacheStore, func()) {
		t.Helper()
		n := counter.Add(1)
		raw, err := cache.NewRedisStore(cache.RedisConfig{
			URL:         connStr,
			UseCluster:  false,
			PoolSize:    5,
			DialTimeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("NewRedisStore: %v", err)
		}
		store := cache.WithNamespace(raw, fmt.Sprintf("test%d", n))
		return store, func() {}
	}

	// nil advanceClock → compliance suite uses real sleeps for TTL test.
	compliance.RunCacheStoreCompliance(t, factory, nil)
}
