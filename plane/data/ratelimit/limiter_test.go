package ratelimit_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	redistc "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/gitscale-platform/gitscale/plane/data/compliance"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
)

// --- Memory impl ---

func TestRateLimiter_Memory_Compliance(t *testing.T) {
	factory := func(t *testing.T) (ratelimit.RateLimiter, func(float64), func()) {
		t.Helper()
		clk := ratelimit.NewFakeClock(time.Now())
		lim := ratelimit.NewMemoryLimiter(clk)
		advance := func(seconds float64) {
			clk.Advance(time.Duration(seconds * float64(time.Second)))
		}
		return lim, advance, func() {}
	}
	compliance.RunRateLimiterCompliance(t, factory)
}

// --- Redis impl ---

func TestRateLimiter_Redis_Compliance(t *testing.T) {
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

	var (
		counterMu sync.Mutex
		counter   int64
	)

	factory := func(t *testing.T) (ratelimit.RateLimiter, func(float64), func()) {
		t.Helper()
		counterMu.Lock()
		counter++
		n := counter
		counterMu.Unlock()
		opt, err := redis.ParseURL(connStr)
		if err != nil {
			t.Fatalf("parse redis URL: %v", err)
		}
		client := redis.NewClient(opt)
		inner := ratelimit.NewRedisLimiter(client)
		lim := ratelimit.WithNamespace(inner, fmt.Sprintf("test%d", n))
		return lim, nil, func() { _ = client.Close() }
	}

	compliance.RunRateLimiterCompliance(t, factory)
}
