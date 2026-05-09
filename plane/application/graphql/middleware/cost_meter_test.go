package middleware_test

import (
	"context"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/middleware"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
	"github.com/google/uuid"
)

func TestCostMeter_AcceptedConsumesFullCost(t *testing.T) {
	t.Parallel()
	lim := ratelimit.NewMemoryLimiter(nil)
	m := &middleware.CostMeter{
		Limiter: lim,
		Params:  middleware.BucketParams{AgentCapacity: 100, AgentRefillPerSec: 0},
	}
	p := middleware.Principal{Kind: middleware.PrincipalAgent, ID: uuid.New()}
	if err := m.Charge(context.Background(), p, middleware.TokensFor(cost.Cost{Complexity: 80}, true)); err != nil {
		t.Fatalf("first charge: %v", err)
	}
	// Second 80-charge should now exhaust → ErrRateLimited.
	err := m.Charge(context.Background(), p, middleware.TokensFor(cost.Cost{Complexity: 80}, true))
	if !middleware.IsRateLimited(err) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
}

func TestCostMeter_RejectedChargesParseCost(t *testing.T) {
	t.Parallel()
	lim := ratelimit.NewMemoryLimiter(nil)
	m := &middleware.CostMeter{
		Limiter: lim,
		Params:  middleware.BucketParams{HumanCapacity: 200, HumanRefillPerSec: 0},
	}
	p := middleware.Principal{Kind: middleware.PrincipalHuman, ID: uuid.New()}
	// Cost 9999 over budget → parse cost = max(20, 999) = 999.
	tokens := middleware.TokensFor(cost.Cost{Complexity: 9999}, false)
	if tokens != 999 {
		t.Errorf("parse-cost = %d, want 999", tokens)
	}
	// 999 against capacity 200 fails the take → ErrRateLimited.
	err := m.Charge(context.Background(), p, tokens)
	if !middleware.IsRateLimited(err) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
}

func TestCostMeter_RejectedSmallQueryFloors20(t *testing.T) {
	t.Parallel()
	lim := ratelimit.NewMemoryLimiter(nil)
	m := &middleware.CostMeter{
		Limiter: lim,
		Params:  middleware.BucketParams{HumanCapacity: 100, HumanRefillPerSec: 0},
	}
	p := middleware.Principal{Kind: middleware.PrincipalHuman, ID: uuid.New()}
	// Small cost should still charge floor of 20.
	tokens := middleware.TokensFor(cost.Cost{Complexity: 1}, false)
	if tokens != 20 {
		t.Errorf("parse-cost floor = %d, want 20", tokens)
	}
	if err := m.Charge(context.Background(), p, tokens); err != nil {
		t.Fatalf("charge: %v", err)
	}
}

func TestCostMeter_ZeroCapacityDisables(t *testing.T) {
	t.Parallel()
	lim := ratelimit.NewMemoryLimiter(nil)
	m := &middleware.CostMeter{Limiter: lim} // no capacity
	p := middleware.Principal{Kind: middleware.PrincipalAgent, ID: uuid.New()}
	if err := m.Charge(context.Background(), p, 1_000_000); err != nil {
		t.Fatalf("zero-capacity should be a no-op, got %v", err)
	}
}
