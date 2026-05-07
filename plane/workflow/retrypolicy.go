package workflow

import (
	"time"

	"go.temporal.io/sdk/temporal"
)

// DefaultRetryPolicy is the project-wide default for activity retries.
// No GitScale activity uses Temporal's default unlimited retries — every
// activity inherits this policy unless explicitly overridden in its
// ActivityOptions.
//
// Five attempts with exponential backoff (1s → 2s → 4s → 8s → 16s, capped
// at 60s) lets a transient blip resolve without paging while still bounding
// the worst case. NonRetryableErrorTypes is intentionally empty here;
// per-activity overrides should set it for application-level errors that
// must propagate immediately (e.g. invariant violations).
func DefaultRetryPolicy() *temporal.RetryPolicy {
	return &temporal.RetryPolicy{
		InitialInterval:    1 * time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    60 * time.Second,
		MaximumAttempts:    5,
	}
}
