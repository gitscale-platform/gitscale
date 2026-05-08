// plane/data/store/billing/archiver.go
package billing

import (
	"context"
	"time"
)

// Archiver is the data-layer interface for the archive workflow activities.
// All methods must be idempotent — the workflow retry policy will replay them.
type Archiver interface {
	// DetachUsageEventsPartition issues ALTER TABLE … DETACH PARTITION … CONCURRENTLY.
	// No-op if the partition is already detached or does not exist as a child.
	DetachUsageEventsPartition(ctx context.Context, year, month int) error

	// DropUsageEventsPartition drops the detached billing.usage_events_YYYY_MM table.
	// Must only be called after the object store upload and outbox emit succeed.
	// Uses DROP TABLE IF EXISTS — idempotent.
	DropUsageEventsPartition(ctx context.Context, year, month int) error

	// ScanPartitionRows returns a cursor over all rows in the named partition.
	// The caller must call Close on the cursor regardless of error.
	ScanPartitionRows(ctx context.Context, year, month int) (RowCursor, error)
}

// RowCursor iterates over rows in a partition. Close must always be called.
type RowCursor interface {
	Next(ctx context.Context) bool
	Row() UsageEventRow
	Err() error
	Close() error
}

// UsageEventRow mirrors the billing.usage_events column set from 005_billing.sql.
// String types are used for UUIDs and the surface enum so the Parquet file is
// readable by Athena/Trino/DuckDB without binary decoding.
// Pointer fields are nullable columns in Parquet (optional in schema).
type UsageEventRow struct {
	ID              string    `parquet:"id"`
	AccountID       string    `parquet:"account_id"`
	PrincipalID     string    `parquet:"principal_id"`
	PrincipalType   string    `parquet:"principal_type"`
	Surface         string    `parquet:"surface"`
	CostVector      string    `parquet:"cost_vector"`
	Value           int64     `parquet:"value"`
	RepoID          *string   `parquet:"repo_id"`
	EventSource     string    `parquet:"event_source"`
	ExternalEventID *string   `parquet:"external_event_id"`
	Ts              time.Time `parquet:"ts"`
	CreatedAt       time.Time `parquet:"created_at"`
}
