// plane/data/store/billing/restorer_postgres.go
package billing

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parquet-go/parquet-go"
)

// PostgresRestorer is the production Restorer backed by pgxpool. It only ever
// touches the quarantine namespace (billing.usage_events_restore_YYYY_MM);
// live-partition writes are out of scope by ADR-019.
type PostgresRestorer struct {
	pool *pgxpool.Pool
}

// NewPostgresRestorer returns a PostgresRestorer. The pool is not owned by
// this struct; the caller is responsible for closing it.
func NewPostgresRestorer(pool *pgxpool.Pool) *PostgresRestorer {
	return &PostgresRestorer{pool: pool}
}

// EnsureQuarantineTable creates billing.usage_events_restore_YYYY_MM if it
// does not already exist, cloning the column set + defaults from
// billing.usage_events. The table is intentionally not attached as a
// partition — it lives outside the partition tree.
func (r *PostgresRestorer) EnsureQuarantineTable(ctx context.Context, year, month int) (string, error) {
	if year < 2026 || year > 2099 {
		return "", fmt.Errorf("restorer: year %d out of range", year)
	}
	if month < 1 || month > 12 {
		return "", fmt.Errorf("restorer: month %d out of range", month)
	}
	table := fmt.Sprintf("usage_events_restore_%04d_%02d", year, month)
	ddl := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS billing.%s "+
			"(LIKE billing.usage_events INCLUDING DEFAULTS)",
		table,
	)
	if _, err := r.pool.Exec(ctx, ddl); err != nil {
		return "", fmt.Errorf("restorer: create quarantine %s: %w", table, err)
	}
	return "billing." + table, nil
}

// LoadParquetIntoQuarantine reads plaintext Parquet rows from src and inserts
// them into the quarantine table via pgx CopyFrom. Returns the total row
// count. The workflow seals the quarantine table after this completes.
//
// Implementation note: parquet-go requires io.ReaderAt + size, so the
// plaintext is buffered to memory. Monthly partitions land in the GB-low-tens
// range at most; activity heap budget is sized to absorb that. If a future
// month exceeds the budget, switch to a temp file (the activity scratch dir
// is ephemeral per worker).
func (r *PostgresRestorer) LoadParquetIntoQuarantine(ctx context.Context, year, month int, src io.Reader) (int64, error) {
	table := fmt.Sprintf("usage_events_restore_%04d_%02d", year, month)

	plaintext, err := io.ReadAll(src)
	if err != nil {
		return 0, fmt.Errorf("restorer: read parquet: %w", err)
	}

	pr := parquet.NewGenericReader[UsageEventRow](bytes.NewReader(plaintext))
	defer func() { _ = pr.Close() }()

	var rows []UsageEventRow
	batch := make([]UsageEventRow, 1024)
	for {
		n, err := pr.Read(batch)
		if n > 0 {
			rows = append(rows, batch[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("restorer: parquet read: %w", err)
		}
	}

	copyCount, err := r.pool.CopyFrom(ctx,
		pgx.Identifier{"billing", table},
		[]string{
			"id", "account_id", "principal_id", "principal_type",
			"surface", "cost_vector", "value", "repo_id",
			"event_source", "external_event_id", "ts", "created_at",
		},
		&pgxCopyFromSource{rows: rows},
	)
	if err != nil {
		return 0, fmt.Errorf("restorer: copy into %s: %w", table, err)
	}
	return copyCount, nil
}

// SealQuarantineTable revokes write privileges so the table is SELECT-only.
// Spec: dispute / audit-restore consumers read; nobody writes after load.
func (r *PostgresRestorer) SealQuarantineTable(ctx context.Context, year, month int) error {
	table := fmt.Sprintf("usage_events_restore_%04d_%02d", year, month)
	stmt := fmt.Sprintf(
		"REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON billing.%s FROM PUBLIC",
		table,
	)
	if _, err := r.pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("restorer: seal %s: %w", table, err)
	}
	return nil
}

// DropQuarantineTable removes the quarantine table — used as compensation
// when the workflow fails after EnsureQuarantineTable succeeded.
func (r *PostgresRestorer) DropQuarantineTable(ctx context.Context, year, month int) error {
	table := fmt.Sprintf("usage_events_restore_%04d_%02d", year, month)
	stmt := fmt.Sprintf("DROP TABLE IF EXISTS billing.%s", table)
	if _, err := r.pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("restorer: drop %s: %w", table, err)
	}
	return nil
}

// pgxCopyFromSource adapts a UsageEventRow slice to pgx.CopyFromSource.
// Column order must mirror the list passed to CopyFrom in
// LoadParquetIntoQuarantine.
type pgxCopyFromSource struct {
	rows []UsageEventRow
	pos  int
}

func (c *pgxCopyFromSource) Next() bool { return c.pos < len(c.rows) }
func (c *pgxCopyFromSource) Values() ([]any, error) {
	r := c.rows[c.pos]
	c.pos++
	return []any{
		r.ID, r.AccountID, r.PrincipalID, r.PrincipalType,
		r.Surface, r.CostVector, r.Value, r.RepoID,
		r.EventSource, r.ExternalEventID, r.Ts, r.CreatedAt,
	}, nil
}
func (c *pgxCopyFromSource) Err() error { return nil }
