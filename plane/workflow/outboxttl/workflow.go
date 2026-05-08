package outboxttl

import (
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/workflow"
)

// ExpireOutboxesResult aggregates per-domain results across the fan-out.
// Errors carries one entry per domain that failed; PerDomain carries one
// entry per domain that succeeded.
type ExpireOutboxesResult struct {
	PerDomain []ExpireDomainResult
	Errors    []string
}

// ExpireOutboxesWorkflow fans out to one ExpireDomainOutboxActivity per
// domain, awaits all, and returns aggregated outcome. If any domain fails,
// the workflow returns an error overall while still surfacing per-domain
// results for the successful domains.
//
// Determinism (ADR-003): the domains slice is fixed in the source and
// futures are awaited in declaration order. No time.Now / random /
// map-iter / goroutine inside the workflow body — all side effects are
// confined to the activity. Replay-safe.
func ExpireOutboxesWorkflow(ctx workflow.Context) (ExpireOutboxesResult, error) {
	domains := []store.Domain{
		store.DomainIdentity,
		store.DomainRepositories,
		store.DomainCollaboration,
		store.DomainCI,
		store.DomainBilling,
	}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	actx := workflow.WithActivityOptions(ctx, ao)

	futures := make([]workflow.Future, len(domains))
	for i, d := range domains {
		futures[i] = workflow.ExecuteActivity(actx, ActivityNameExpireDomainOutbox,
			ExpireDomainInput{Domain: string(d)})
	}

	var totals ExpireOutboxesResult
	for i, f := range futures {
		var r ExpireDomainResult
		if err := f.Get(ctx, &r); err != nil {
			totals.Errors = append(totals.Errors, fmt.Sprintf("%s: %v", domains[i], err))
			continue
		}
		totals.PerDomain = append(totals.PerDomain, r)
	}
	if len(totals.Errors) > 0 {
		return totals, fmt.Errorf("expirer: %d domain(s) failed", len(totals.Errors))
	}
	return totals, nil
}
