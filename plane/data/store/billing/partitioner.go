// Package billing exposes the billing-domain maintenance interfaces consumed
// by the workflow plane (#18-rollover, #18-archive). Kept separate from
// plane/data/store/MetadataStore so DDL surface area does not leak into the
// CRUD interface honoured by the application plane (ADR-017).
package billing

import "context"

// Partitioner creates monthly range partitions on billing.usage_events.
// Implementations must be idempotent on (year, month) — the workflow's
// retry policy will replay the activity on transient errors.
type Partitioner interface {
	// CreateUsageEventsPartition creates billing.usage_events_<year>_<month>
	// for the calendar month [month, next_month). Idempotent: a second call
	// with the same (year, month) is a no-op and returns nil.
	CreateUsageEventsPartition(ctx context.Context, year, month int) (created bool, err error)
}
