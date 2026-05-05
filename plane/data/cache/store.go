// Package cache defines the CacheStore interface and its implementations.
// All Redis-specific code is confined to store_redis.go; application code
// imports only this file (ADR-017).
package cache

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Get when the key does not exist in the store.
var ErrNotFound = errors.New("cache: key not found")

// CacheStore is the interface for transient key-value storage with TTLs.
// Implementations: RedisStore (prod), MemoryStore (test/dev).
// Wired at startup; never pass a concrete type across plane boundaries.
type CacheStore interface {
	// Get returns the cached bytes, or (nil, ErrNotFound) on a miss.
	Get(ctx context.Context, key string) ([]byte, error)

	// MGet returns one slot per requested key, in order. nil entries are misses.
	MGet(ctx context.Context, keys []string) ([][]byte, error)

	// Set stores value with the given TTL. Single round-trip (SET … PX).
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// Delete removes the key. No-op if the key does not exist.
	Delete(ctx context.Context, key string) error

	// CompareAndSwap sets key=replacement only when the current value equals
	// expected. Returns true if the swap happened, false on mismatch.
	// ttl is applied to the key on a successful swap.
	// Implemented as a single Lua round-trip (cluster-safe).
	CompareAndSwap(ctx context.Context, key string, expected, replacement []byte, ttl time.Duration) (bool, error)

	// Ping verifies connectivity. Returns nil on success.
	Ping(ctx context.Context) error
}
