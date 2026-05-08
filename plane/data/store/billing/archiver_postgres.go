package billing

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresArchiver is the production Archiver backed by pgxpool.
type PostgresArchiver struct {
	pool *pgxpool.Pool
}

// NewPostgresArchiver returns a PostgresArchiver. The pool is not owned by
// this struct; the caller is responsible for closing it.
func NewPostgresArchiver(pool *pgxpool.Pool) *PostgresArchiver {
	return &PostgresArchiver{pool: pool}
}

// DetachUsageEventsPartition detaches the named partition from billing.usage_events.
// Idempotent: checks pg_inherits first; no-op if already detached.
func (a *PostgresArchiver) DetachUsageEventsPartition(ctx context.Context, year, month int) error {
	tableName := fmt.Sprintf("usage_events_%04d_%02d", year, month)

	var attached, pending bool
	err := a.pool.QueryRow(ctx, `
		SELECT
		  EXISTS (
		    SELECT 1 FROM pg_inherits
		    JOIN pg_class parent ON pg_inherits.inhparent = parent.oid
		    JOIN pg_class child  ON pg_inherits.inhrelid  = child.oid
		    JOIN pg_namespace ns ON parent.relnamespace   = ns.oid
		    WHERE ns.nspname    = 'billing'
		      AND parent.relname = 'usage_events'
		      AND child.relname  = $1
		  ) AS attached,
		  COALESCE((
		    SELECT inhdetachpending FROM pg_inherits
		    JOIN pg_class parent ON pg_inherits.inhparent = parent.oid
		    JOIN pg_class child  ON pg_inherits.inhrelid  = child.oid
		    JOIN pg_namespace ns ON parent.relnamespace   = ns.oid
		    WHERE ns.nspname    = 'billing'
		      AND parent.relname = 'usage_events'
		      AND child.relname  = $1
		  ), false) AS pending`, tableName,
	).Scan(&attached, &pending)
	if err != nil {
		return fmt.Errorf("archiver: probe pg_inherits: %w", err)
	}
	if !attached {
		return nil // already detached
	}
	if pending {
		// Recover from a previously-interrupted DETACH CONCURRENTLY:
		// pg_inherits row still exists with inhdetachpending=true. A re-issued
		// DETACH CONCURRENTLY would fail; FINALIZE completes the detach.
		_, err = a.pool.Exec(ctx, fmt.Sprintf(
			"ALTER TABLE billing.usage_events DETACH PARTITION billing.%s FINALIZE",
			tableName,
		))
		if err != nil {
			return fmt.Errorf("archiver: finalize detach %s: %w", tableName, err)
		}
		return nil
	}

	_, err = a.pool.Exec(ctx, fmt.Sprintf(
		"ALTER TABLE billing.usage_events DETACH PARTITION billing.%s CONCURRENTLY",
		tableName,
	))
	if err != nil {
		return fmt.Errorf("archiver: detach %s: %w", tableName, err)
	}
	return nil
}

// DropUsageEventsPartition drops the detached partition table.
// Uses DROP TABLE IF EXISTS — idempotent.
func (a *PostgresArchiver) DropUsageEventsPartition(ctx context.Context, year, month int) error {
	tableName := fmt.Sprintf("usage_events_%04d_%02d", year, month)
	_, err := a.pool.Exec(ctx,
		fmt.Sprintf("DROP TABLE IF EXISTS billing.%s", tableName),
	)
	if err != nil {
		return fmt.Errorf("archiver: drop %s: %w", tableName, err)
	}
	return nil
}

// ScanPartitionRows opens a server-side cursor over all rows in the detached
// partition, ordered by (ts, id). The caller must call Close.
func (a *PostgresArchiver) ScanPartitionRows(ctx context.Context, year, month int) (RowCursor, error) {
	tableName := fmt.Sprintf("usage_events_%04d_%02d", year, month)
	query := fmt.Sprintf(`
		SELECT id::text, account_id::text, principal_id::text, principal_type,
		       surface::text, cost_vector::text, value, repo_id::text,
		       event_source, external_event_id::text, ts, created_at
		FROM billing.%s
		ORDER BY ts, id`, tableName)

	rows, err := a.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("archiver: scan %s: %w", tableName, err)
	}
	return &pgxCursor{rows: rows}, nil
}

type pgxCursor struct {
	rows pgx.Rows
	cur  UsageEventRow
	err  error
}

func (c *pgxCursor) Next(ctx context.Context) bool {
	// Short-circuit on context cancellation: pgx.Rows.Next() does not check
	// ctx, so without this an in-flight scan can run for a long time after
	// Temporal cancels the activity (heartbeat timeout, worker shutdown).
	if err := ctx.Err(); err != nil {
		c.err = err
		return false
	}
	if !c.rows.Next() {
		return false
	}
	var repoID, extID *string
	c.err = c.rows.Scan(
		&c.cur.ID, &c.cur.AccountID, &c.cur.PrincipalID, &c.cur.PrincipalType,
		&c.cur.Surface, &c.cur.CostVector, &c.cur.Value, &repoID,
		&c.cur.EventSource, &extID, &c.cur.Ts, &c.cur.CreatedAt,
	)
	c.cur.RepoID = repoID
	c.cur.ExternalEventID = extID
	return c.err == nil
}

func (c *pgxCursor) Row() UsageEventRow { return c.cur }
func (c *pgxCursor) Err() error {
	if c.err != nil {
		return c.err
	}
	return c.rows.Err()
}
func (c *pgxCursor) Close() error {
	c.rows.Close()
	return nil
}
