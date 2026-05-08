package outboxttl

import (
	"context"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/store"
)

// ActivityNameExpireDomainOutbox is the registered Temporal name for
// ExpireDomainOutboxActivity.Execute. The workflow dispatches by this
// string so tests can register a fake activity under the same name
// without depending on the real Expirer.
const ActivityNameExpireDomainOutbox = "outbox.ExpireDomain"

// ExpireDomainInput names the domain whose outbox is to be expired.
// String form (not store.Domain) so the activity payload survives
// JSON round-trip without registering a custom encoder.
type ExpireDomainInput struct {
	Domain string
}

// ExpireDomainResult reports a single domain's expirer outcome.
type ExpireDomainResult struct {
	Domain      string
	RowsDeleted int64
	DurationMS  int64
}

// ExpireDomainOutboxActivity dispatches per-domain Expirer.Expire calls.
// One instance is registered with the worker; the activity holds a map of
// per-domain Expirers wired at boot time.
type ExpireDomainOutboxActivity struct {
	expirers map[store.Domain]*outbox.Expirer
}

// NewExpireDomainOutboxActivity wraps a per-domain expirer map. The map
// keys must be valid store.Domain values; missing entries cause Execute
// to return an error for that domain rather than silently no-op'ing.
func NewExpireDomainOutboxActivity(expirers map[store.Domain]*outbox.Expirer) *ExpireDomainOutboxActivity {
	return &ExpireDomainOutboxActivity{expirers: expirers}
}

// Execute runs the named domain's Expirer. Errors surface to Temporal so
// the workflow can record per-domain failure without aborting the others.
// Idempotent: re-running with no expired rows is a zero-deletion no-op.
func (a *ExpireDomainOutboxActivity) Execute(ctx context.Context, in ExpireDomainInput) (ExpireDomainResult, error) {
	d := store.Domain(in.Domain)
	if !d.Valid() {
		return ExpireDomainResult{}, fmt.Errorf("outboxttl: invalid domain %q", in.Domain)
	}
	exp, ok := a.expirers[d]
	if !ok || exp == nil {
		return ExpireDomainResult{}, fmt.Errorf("outboxttl: no expirer registered for domain %q", in.Domain)
	}
	start := time.Now()
	deleted, err := exp.Expire(ctx)
	if err != nil {
		return ExpireDomainResult{}, fmt.Errorf("outboxttl: expire %s: %w", d, err)
	}
	return ExpireDomainResult{
		Domain:      string(d),
		RowsDeleted: deleted,
		DurationMS:  time.Since(start).Milliseconds(),
	}, nil
}
