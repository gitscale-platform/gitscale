// Package sink defines the AnalyticsSink contract — the consumer of metering
// events drained from the git outbox by the existing per-domain outbox
// consumer (plane/data/outbox). The production implementation is a
// ClickHouse client that lands in Phase 3; this package ships an interface
// and an in-memory StubSink useful for local dev and tests.
//
// The sink is the load-bearing reconciliation store referenced by ADR-015:
// the < 0.5% drift SLA between the Tier-1 Redis enforcement counter and
// Tier-2 totals is asserted against the rows this sink accepts.
package sink

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/git/metering"
)

// AnalyticsSink receives metering events drained from the git outbox.
// Implementations must be safe for concurrent use and idempotent on
// MeteringEvent.EventID — the outbox consumer may replay the same event
// after a crash.
type AnalyticsSink interface {
	Record(ctx context.Context, e metering.MeteringEvent) error
}
