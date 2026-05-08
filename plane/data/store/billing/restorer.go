// plane/data/store/billing/restorer.go
package billing

import (
	"context"
	"fmt"
	"io"
)

// Restorer is the data-layer interface for the partition-restore workflow.
// All writes are confined to the billing.usage_events_restore_YYYY_MM
// quarantine namespace — the live partition tree (billing.usage_events and
// its attached children) is strictly read-only from the restore plane.
//
// ADR-019 amendment: workflow plane importing data plane is permitted for
// read paths and quarantine-table DDL only. Live-partition writes remain
// the application plane's responsibility.
type Restorer interface {
	// EnsureQuarantineTable creates billing.usage_events_restore_YYYY_MM as
	// a standalone (non-partitioned, non-attached) clone of the
	// billing.usage_events column set. Idempotent.
	EnsureQuarantineTable(ctx context.Context, year, month int) (table string, err error)

	// LoadParquetIntoQuarantine streams plaintext Parquet rows from r into
	// the quarantine table created by EnsureQuarantineTable. Returns the
	// number of rows loaded.
	LoadParquetIntoQuarantine(ctx context.Context, year, month int, r io.Reader) (rows int64, err error)

	// SealQuarantineTable revokes INSERT/UPDATE/DELETE on the table from
	// the application role, leaving SELECT only — the table is read-only by
	// design (audit / dispute investigation, not live billing).
	SealQuarantineTable(ctx context.Context, year, month int) error

	// DropQuarantineTable removes the quarantine table on workflow failure
	// for cleanup. Idempotent (DROP TABLE IF EXISTS).
	DropQuarantineTable(ctx context.Context, year, month int) error
}

// QuarantineTableName returns the canonical quarantine table identifier for
// (year, month). Exported so callers (including activity errors and runbook
// references) share one source of truth.
func QuarantineTableName(year, month int) string {
	return fmt.Sprintf("billing.usage_events_restore_%04d_%02d", year, month)
}
