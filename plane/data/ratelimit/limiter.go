// Package ratelimit defines the RateLimiter interface and its implementations.
// Redis implementation uses a token-bucket Lua script for atomic take-or-reject.
// Memory implementation uses the same algorithm behind a sync.Mutex.
// Both are wired at startup; application code imports only this interface (ADR-017).
package ratelimit

import "context"

// BucketState is a snapshot of a token-bucket's parameters and current
// level. Returned by RateLimiter.Inspect for read-only callers (e.g. the
// MCP `quota_status` tool, #112). The structure is intentionally a value
// type so callers cannot mutate live limiter state.
type BucketState struct {
	// Capacity is the maximum number of tokens the bucket can hold.
	Capacity float64
	// Remaining is the current token level. Zero means the bucket is
	// drained and the next Take will be denied unless refill catches up
	// first.
	Remaining float64
	// RefillPerSec is the configured refill rate in tokens per second.
	// Zero means "no refill" (test/dev shape) — the bucket will not
	// recover after exhaustion.
	RefillPerSec float64
	// Surface is the bucket-key surface (e.g. "mcp", "rest_api") that
	// this state belongs to. Carried so the MCP `quota_status` response
	// can surface it without re-deriving from the key.
	Surface string
}

// RateLimiter is the interface for token-bucket rate limiting.
// Implementations: RedisLimiter (prod), MemoryLimiter (test/dev).
type RateLimiter interface {
	// Take attempts to consume n tokens from the bucket identified by key.
	// Returns granted=true if the tokens were available, false if denied.
	// remaining is the token count left in the bucket after this call.
	//
	// capacity:     maximum number of tokens the bucket can hold
	// refillPerSec: tokens added per second up to capacity
	// n:            tokens to consume (typically 1; agent batch ops may take >1)
	//
	// The take-or-reject decision is atomic: Lua on Redis, sync.Mutex on memory.
	Take(ctx context.Context, key string, capacity, refillPerSec, n float64) (granted bool, remaining float64, err error)

	// Inspect returns a read-only snapshot of the bucket identified by
	// key without consuming tokens. If the bucket has no recorded state
	// (no prior Take), the implementation returns the zero BucketState
	// with no error — callers treat that as "bucket not yet initialised"
	// and either fall back to defaults or skip reporting.
	//
	// Inspect is the surface backing the MCP `quota_status` tool (#112).
	// Adding it to this interface is an additive swap-surface change
	// (ADR-009 / ADR-017): existing callers that only use Take continue
	// to compile.
	Inspect(ctx context.Context, key string) (BucketState, error)
}
