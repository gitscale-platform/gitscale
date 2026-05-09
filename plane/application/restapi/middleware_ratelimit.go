package restapi

import (
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
)

// rateLimitSurface is the surface enum stamped into the bucket key. New
// surfaces (mcp, graphql) get their own constant in their own packages so
// REST-only spikes don't bleed into other planes' budgets.
const rateLimitSurface = "rest_api"

// RateConfig parameters the per-principal token-bucket. Zero values mean
// unlimited so tests that don't care about rate-limiting can pass a zero
// value and disable enforcement.
type RateConfig struct {
	AgentCapacity     float64
	AgentRefillPerSec float64
	HumanCapacity     float64
	HumanRefillPerSec float64
}

// rateLimitSkipPaths bypass the limiter (mirrors authSkipPaths).
var rateLimitSkipPaths = map[string]struct{}{
	"/healthz": {},
}

// rateLimitMiddleware enforces a per-principal token bucket. It must run
// AFTER auth so unauthenticated traffic does not consume buckets attached
// to a real principal (a hostile actor could otherwise spam an empty
// token to drain the legitimate user's bucket — but the bucket key is
// derived from the resolved principal id, not from the bearer string,
// closing that loop).
func rateLimitMiddleware(limiter ratelimit.RateLimiter, cfg RateConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, skip := rateLimitSkipPaths[r.URL.Path]; skip {
				next.ServeHTTP(w, r)
				return
			}
			p := PrincipalFromContext(r.Context())
			if p == nil {
				// Auth must always run before rate-limit. Defensive guard.
				writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "missing principal")
				return
			}
			capacity, refill := bucketParams(cfg, p.Kind())
			if capacity <= 0 || refill < 0 {
				next.ServeHTTP(w, r)
				return
			}
			key := fmt.Sprintf(ratelimit.TokenBucketKey, p.ID(), rateLimitSurface)
			granted, _, err := limiter.Take(r.Context(), key, capacity, refill, 1)
			if err != nil {
				// Fail closed: a limiter outage must not become an open door.
				writeError(w, r, http.StatusInternalServerError, CodeInternal, "rate-limit backend error")
				return
			}
			if !granted {
				w.Header().Set("Retry-After", retryAfter(refill))
				writeError(w, r, http.StatusTooManyRequests, CodeRateLimited, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bucketParams(cfg RateConfig, kind PrincipalKind) (capacity, refill float64) {
	switch kind {
	case PrincipalAgent:
		return cfg.AgentCapacity, cfg.AgentRefillPerSec
	case PrincipalHuman:
		return cfg.HumanCapacity, cfg.HumanRefillPerSec
	default:
		return 0, 0
	}
}

// retryAfter returns the seconds-until-one-token header value as a string.
// When refill is zero (test shape) it returns "1" so clients backoff at
// least one second instead of busy-looping.
func retryAfter(refillPerSec float64) string {
	if refillPerSec <= 0 {
		return "1"
	}
	secs := int(math.Ceil(1.0 / refillPerSec))
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}
