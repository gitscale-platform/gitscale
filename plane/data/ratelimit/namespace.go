package ratelimit

import "context"

// namespacedLimiter prepends "gitscale:<env>:" to every key before delegating
// to the inner RateLimiter. Mirrors cache.WithNamespace (ADR-009).
type namespacedLimiter struct {
	inner  RateLimiter
	prefix string
}

// WithNamespace wraps inner with an env-specific key prefix.
// Construct once at startup; rest of code passes raw keys.
func WithNamespace(inner RateLimiter, env string) RateLimiter {
	return &namespacedLimiter{inner: inner, prefix: "gitscale:" + env + ":"}
}

func (n *namespacedLimiter) Take(ctx context.Context, key string, capacity, refillPerSec, take float64) (bool, float64, error) {
	return n.inner.Take(ctx, n.prefix+key, capacity, refillPerSec, take)
}
