package ratelimit

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed lua/token_bucket.lua
var tokenBucketLuaScript string

//go:embed lua/token_bucket_inspect.lua
var tokenBucketInspectLuaScript string

var (
	tokenBucketScript        = redis.NewScript(tokenBucketLuaScript)
	tokenBucketInspectScript = redis.NewScript(tokenBucketInspectLuaScript)
)

// RedisLimiter implements RateLimiter using a Redis Lua token-bucket script.
// One round-trip per Take call; cluster-safe (single key per call).
type RedisLimiter struct {
	client redis.UniversalClient
}

// NewRedisLimiter wraps an existing go-redis universal client.
// The client may back a single Redis node or a Cluster — the Lua script is
// cluster-safe because it operates on a single key per invocation.
func NewRedisLimiter(client redis.UniversalClient) *RedisLimiter {
	return &RedisLimiter{client: client}
}

func (r *RedisLimiter) Take(ctx context.Context, key string, capacity, refillPerSec, n float64) (bool, float64, error) {
	nowMs := time.Now().UnixMilli()
	// ttlMs: 2× the time to fully refill the bucket from empty. Guarantees the
	// key outlives any legitimate burst window.
	// When refillPerSec is 0 (tests with no refill), fall back to a fixed 60s TTL.
	var ttlMs int64
	if refillPerSec > 0 {
		ttlMs = int64(2 * (capacity / refillPerSec) * 1000)
	} else {
		ttlMs = 60_000
	}
	if ttlMs < 1000 {
		ttlMs = 1000 // minimum 1s to avoid evicting live buckets under load
	}

	result, err := tokenBucketScript.Run(
		ctx,
		r.client,
		[]string{key},
		capacity,
		refillPerSec,
		nowMs,
		n,
		ttlMs,
	).Slice()
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit: take %q: %w", key, err)
	}
	if len(result) < 2 {
		return false, 0, fmt.Errorf("ratelimit: unexpected script result length %d", len(result))
	}

	granted, ok := result[0].(int64)
	if !ok {
		return false, 0, fmt.Errorf("ratelimit: unexpected granted type %T", result[0])
	}
	remainStr, ok := result[1].(string)
	if !ok {
		return false, 0, fmt.Errorf("ratelimit: unexpected remaining type %T", result[1])
	}
	remaining, err := strconv.ParseFloat(remainStr, 64)
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit: parse remaining %q: %w", remainStr, err)
	}

	return granted == 1, remaining, nil
}

// Inspect issues a read-only HMGET against the bucket key and returns
// the recorded {capacity, tokens, refill}. An absent key yields a zero
// BucketState (callers fall back to defaults). The script is cluster-safe
// because it operates on a single key.
func (r *RedisLimiter) Inspect(ctx context.Context, key string) (BucketState, error) {
	result, err := tokenBucketInspectScript.Run(
		ctx,
		r.client,
		[]string{key},
	).Slice()
	if err != nil {
		return BucketState{}, fmt.Errorf("ratelimit: inspect %q: %w", key, err)
	}
	if len(result) < 3 {
		return BucketState{}, fmt.Errorf("ratelimit: unexpected inspect length %d", len(result))
	}
	capStr, ok := result[0].(string)
	if !ok {
		return BucketState{}, fmt.Errorf("ratelimit: unexpected capacity type %T", result[0])
	}
	tokStr, ok := result[1].(string)
	if !ok {
		return BucketState{}, fmt.Errorf("ratelimit: unexpected tokens type %T", result[1])
	}
	refStr, ok := result[2].(string)
	if !ok {
		return BucketState{}, fmt.Errorf("ratelimit: unexpected refill type %T", result[2])
	}
	capacity, err := strconv.ParseFloat(capStr, 64)
	if err != nil {
		return BucketState{}, fmt.Errorf("ratelimit: parse capacity %q: %w", capStr, err)
	}
	tokens, err := strconv.ParseFloat(tokStr, 64)
	if err != nil {
		return BucketState{}, fmt.Errorf("ratelimit: parse tokens %q: %w", tokStr, err)
	}
	refill, err := strconv.ParseFloat(refStr, 64)
	if err != nil {
		return BucketState{}, fmt.Errorf("ratelimit: parse refill %q: %w", refStr, err)
	}
	if capacity == 0 && tokens == 0 && refill == 0 {
		// Treat all-zero as "no recorded state" so callers see the same
		// zero-value shape they'd get from MemoryLimiter on a missing key.
		return BucketState{}, nil
	}
	return BucketState{
		Capacity:     capacity,
		Remaining:    tokens,
		RefillPerSec: refill,
	}, nil
}

// ensure RedisLimiter satisfies RateLimiter at compile time.
var _ RateLimiter = (*RedisLimiter)(nil)
