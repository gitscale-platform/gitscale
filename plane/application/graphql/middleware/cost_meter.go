package middleware

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
)

// SurfaceKey is the rate-limit surface for GraphQL traffic. Distinct from
// rest_api so REST and GraphQL budgets are independent — agents can
// saturate one without starving the other.
const SurfaceKey = "graphql"

// ErrRateLimited is returned by CostMeter.Charge when the bucket is
// exhausted. RetrySeconds carries the suggested backoff.
type ErrRateLimited struct {
	RetrySeconds int
}

func (e ErrRateLimited) Error() string {
	return "graphql: rate limited; retry in " + strconv.Itoa(e.RetrySeconds) + "s"
}

// IsRateLimited returns true when err is or wraps ErrRateLimited.
func IsRateLimited(err error) bool {
	var e ErrRateLimited
	return errors.As(err, &e)
}

// BucketParams parameterises the per-principal token bucket. Zero values
// disable enforcement (matching restapi RateConfig semantics).
type BucketParams struct {
	AgentCapacity     float64
	AgentRefillPerSec float64
	HumanCapacity     float64
	HumanRefillPerSec float64
}

// CostMeter charges per-query cost-as-tokens against the per-principal
// graphql bucket. It is invoked twice per accepted request — once with
// the parse-cost when analysis rejects, once with the full cost otherwise
// — through Charge().
type CostMeter struct {
	Limiter ratelimit.RateLimiter
	Params  BucketParams
}

// Charge takes `tokens` tokens from p's bucket. accepted is informational
// for log correlation; the bucket charge is identical regardless. Returns
// ErrRateLimited{} when the bucket is exhausted.
//
// When the limiter back-end errors transient, Charge returns the wrapped
// error — fail-closed semantics matching the REST middleware.
func (m *CostMeter) Charge(ctx context.Context, p Principal, tokens int) error {
	cap, refill := m.bucketParams(p.Kind)
	if cap <= 0 {
		return nil
	}
	if tokens < 1 {
		tokens = 1
	}
	key := fmt.Sprintf(ratelimit.TokenBucketKey, p.ID, SurfaceKey)
	granted, _, err := m.Limiter.Take(ctx, key, cap, refill, float64(tokens))
	if err != nil {
		return fmt.Errorf("ratelimit: %w", err)
	}
	if !granted {
		return ErrRateLimited{RetrySeconds: retrySeconds(refill, tokens)}
	}
	return nil
}

func (m *CostMeter) bucketParams(kind PrincipalKind) (capacity, refill float64) {
	switch kind {
	case PrincipalAgent:
		return m.Params.AgentCapacity, m.Params.AgentRefillPerSec
	case PrincipalHuman:
		return m.Params.HumanCapacity, m.Params.HumanRefillPerSec
	default:
		return 0, 0
	}
}

func retrySeconds(refill float64, tokens int) int {
	if refill <= 0 {
		return 1
	}
	secs := int(math.Ceil(float64(tokens) / refill))
	if secs < 1 {
		secs = 1
	}
	return secs
}

// TokensFor returns the bucket charge for an analyzed query.
//
//	accepted = true  → cost.Complexity tokens
//	accepted = false → ParseCost(cost) tokens
//
// The split deters probe-floods: a rejected query is not free, but it is
// cheap enough that legitimate over-budget callers can adjust without
// burning their full hourly budget on a single typo.
func TokensFor(c cost.Cost, accepted bool) int {
	if accepted {
		return c.Complexity
	}
	return cost.ParseCost(c)
}
