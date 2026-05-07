// Package canary holds the workflow + activity used to verify worker boot
// in tests and during operational smoke checks. The canary reads a single
// well-known cache key and returns its value; success proves that the
// worker is registered, can dispatch activities, can reach the cache, and
// produces deterministic history under replay.
package canary

import (
	"context"
	"errors"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
)

// HealthKey is the cache key the canary reads. Stored under the project's
// standard namespace (gitscale:<env>:); the activity passes it to
// cache.CacheStore.Get without namespace prefix per cache.WithNamespace
// convention.
const HealthKey = "workflow:health"

// ErrCacheMiss is returned by the activity when the health key is absent.
// Workflow callers treat this as a transient state — the canary is wired,
// the operator just hasn't seeded the key yet.
var ErrCacheMiss = errors.New("canary: workflow:health key absent in cache")

// HealthActivity reads HealthKey from the cache and returns its string value.
// Read-only; safe to call from any task queue. Activity files are exempt
// from the determinism lint per ADR-003 / spec D5.
type HealthActivity struct {
	Cache cache.CacheStore
}

// NewHealthActivity returns an activity bound to c. Construction-time
// injection keeps the activity function signature pure for Temporal's
// reflection-based registration.
func NewHealthActivity(c cache.CacheStore) *HealthActivity { return &HealthActivity{Cache: c} }

// Run reads HealthKey and returns its value. Returns ErrCacheMiss on miss
// (distinguishable from infrastructure errors).
func (a *HealthActivity) Run(ctx context.Context) (string, error) {
	val, err := a.Cache.Get(ctx, HealthKey)
	if err != nil {
		if errors.Is(err, cache.ErrNotFound) {
			return "", ErrCacheMiss
		}
		return "", err
	}
	return string(val), nil
}
