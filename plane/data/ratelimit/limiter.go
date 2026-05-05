// Package ratelimit defines the RateLimiter interface and its implementations.
// Redis implementation uses a token-bucket Lua script for atomic take-or-reject.
// Memory implementation uses the same algorithm behind a sync.Mutex.
// Both are wired at startup; application code imports only this interface (ADR-017).
package ratelimit

import "context"

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
}
