package billing

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresPartitioner is the production implementation backed by pgxpool.
type PostgresPartitioner struct {
	pool *pgxpool.Pool
}

// NewPostgresPartitioner returns a Partitioner backed by pool. The pool is
// not owned by this struct; the caller is responsible for closing it.
func NewPostgresPartitioner(pool *pgxpool.Pool) *PostgresPartitioner {
	return &PostgresPartitioner{pool: pool}
}

// CreateUsageEventsPartition serialises concurrent retries via a session-level
// advisory lock keyed off the partition table name, then issues an idempotent
// CREATE TABLE … IF NOT EXISTS PARTITION OF.
//
// Returns created=true when the DDL produced a new table (best-effort: pgx
// reports CommandTag "CREATE TABLE" both for IF-NOT-EXISTS no-ops and real
// creates, so we additionally probe pg_class to disambiguate for the
// observability metric in the workflow).
func (p *PostgresPartitioner) CreateUsageEventsPartition(ctx context.Context, year, month int) (bool, error) {
	if year < 2026 || year > 2099 {
		return false, fmt.Errorf("billing.CreateUsageEventsPartition: year %d out of supported range", year)
	}
	if month < 1 || month > 12 {
		return false, fmt.Errorf("billing.CreateUsageEventsPartition: month %d out of range", month)
	}

	tableName := fmt.Sprintf("usage_events_%04d_%02d", year, month)
	startDate := fmt.Sprintf("%04d-%02d-01", year, month)
	nextYear, nextMonth := year, month+1
	if nextMonth > 12 {
		nextMonth = 1
		nextYear++
	}
	endDate := fmt.Sprintf("%04d-%02d-01", nextYear, nextMonth)

	lockKey := advisoryLockKey(tableName)

	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("billing: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
		return false, fmt.Errorf("billing: advisory_lock: %w", err)
	}

	var existedBefore bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM pg_class c
		   JOIN pg_namespace n ON n.oid = c.relnamespace
		   WHERE n.nspname = 'billing' AND c.relname = $1
		 )`, tableName,
	).Scan(&existedBefore); err != nil {
		return false, fmt.Errorf("billing: probe pg_class: %w", err)
	}

	ddl := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS billing.%s
		 PARTITION OF billing.usage_events
		 FOR VALUES FROM ('%s') TO ('%s')`,
		tableName, startDate, endDate,
	)
	if _, err := tx.Exec(ctx, ddl); err != nil {
		return false, fmt.Errorf("billing: create partition %s: %w", tableName, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("billing: commit: %w", err)
	}
	return !existedBefore, nil
}

// advisoryLockKey hashes the partition table name to a stable int64 keyspace
// for pg_advisory_xact_lock. fnv-1a 64 is sufficient — collisions only delay
// concurrent rollover attempts, never corrupt state (DDL is idempotent).
func advisoryLockKey(tableName string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(tableName))
	// Cast through uint64 → int64 to preserve all 64 bits.
	return int64(h.Sum64()) //nolint:gosec
}

