package ratelimit

import "context"

// Surface enumerates the rate-limit surfaces stamped into bucket keys. New
// surfaces (mcp, graphql) get their own constant here so per-plane spikes
// don't bleed into other planes' budgets.
const (
	// SurfaceRESTAPI is the bucket surface for the application-plane HTTP
	// API (#111). Each principal has its own bucket per surface.
	SurfaceRESTAPI = "rest_api"

	// SurfaceMCP is the bucket surface for the application-plane MCP
	// server (#112). Distinct from SurfaceRESTAPI so MCP traffic does not
	// starve REST clients (and vice-versa).
	SurfaceMCP = "mcp"
)

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

func (n *namespacedLimiter) Inspect(ctx context.Context, key string) (BucketState, error) {
	return n.inner.Inspect(ctx, n.prefix+key)
}
