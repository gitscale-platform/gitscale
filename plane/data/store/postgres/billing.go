package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// billingReader implements store.BillingReader against the shared querier
// (pgxpool.Pool outside Tx, pgx.Tx inside).
type billingReader struct {
	q querier
}

func (r *billingReader) GetPartitionArchiveByKey(ctx context.Context, year, month int, partitionName string) (*store.PartitionArchive, error) {
	const q = `
		SELECT id, year, month, partition_name, lake_uri,
		       row_count, bytes_written, archived_at
		FROM billing.partition_archives
		WHERE year = $1 AND month = $2 AND partition_name = $3`
	var pa store.PartitionArchive
	err := r.q.QueryRow(ctx, q, year, month, partitionName).Scan(
		&pa.ID, &pa.Year, &pa.Month, &pa.PartitionName, &pa.LakeURI,
		&pa.RowCount, &pa.BytesWritten, &pa.ArchivedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres: GetPartitionArchiveByKey: %w", err)
	}
	return &pa, nil
}

// ListPartitionArchivesArchivedBefore enumerates partition_archives older
// than cutoff for the DEK destruction workflow (#80). Sorted by (year, month,
// id) for deterministic workflow iteration.
func (r *billingReader) ListPartitionArchivesArchivedBefore(ctx context.Context, cutoff time.Time) ([]store.PartitionArchive, error) {
	const q = `
		SELECT id, year, month, partition_name, lake_uri,
		       row_count, bytes_written, archived_at
		FROM billing.partition_archives
		WHERE archived_at < $1
		ORDER BY year ASC, month ASC, id ASC`
	rows, err := r.q.Query(ctx, q, cutoff)
	if err != nil {
		return nil, fmt.Errorf("postgres: ListPartitionArchivesArchivedBefore: %w", err)
	}
	defer rows.Close()
	var out []store.PartitionArchive
	for rows.Next() {
		var pa store.PartitionArchive
		if err := rows.Scan(
			&pa.ID, &pa.Year, &pa.Month, &pa.PartitionName, &pa.LakeURI,
			&pa.RowCount, &pa.BytesWritten, &pa.ArchivedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: ListPartitionArchivesArchivedBefore scan: %w", err)
		}
		out = append(out, pa)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: ListPartitionArchivesArchivedBefore rows: %w", err)
	}
	return out, nil
}

// GetQuotaAccountByOrg returns the billing.quota_accounts row for orgID
// or (nil, nil) when no row exists. CI boot activities use this to enforce
// per-job ceilings (#110, ADR-019).
func (r *billingReader) GetQuotaAccountByOrg(ctx context.Context, orgID uuid.UUID) (*store.QuotaAccount, error) {
	const q = `
		SELECT id, org_id, plan_tier,
		       tokens_per_week_cap, compute_minutes_per_month_cap, storage_gb_cap,
		       created_at, updated_at
		FROM billing.quota_accounts
		WHERE org_id = $1`
	var qa store.QuotaAccount
	err := r.q.QueryRow(ctx, q, orgID).Scan(
		&qa.ID, &qa.OrgID, &qa.PlanTier,
		&qa.TokensPerWeekCap, &qa.ComputeMinutesPerMonthCap, &qa.StorageGBCap,
		&qa.CreatedAt, &qa.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres: GetQuotaAccountByOrg: %w", err)
	}
	return &qa, nil
}

// HasOutboxEventForAggregate consults billing.billing_outbox for an existing
// row keyed by (event_type, aggregate_id). Used by the DEK destruction
// workflow (#80) to make outbox writes idempotent without a source row.
func (r *billingReader) HasOutboxEventForAggregate(ctx context.Context, eventType string, aggregateID uuid.UUID) (bool, error) {
	const q = `
		SELECT 1 FROM billing.billing_outbox
		WHERE event_type = $1 AND aggregate_id = $2
		LIMIT 1`
	var dummy int
	err := r.q.QueryRow(ctx, q, eventType, aggregateID).Scan(&dummy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("postgres: HasOutboxEventForAggregate: %w", err)
	}
	return true, nil
}

// billingWriter embeds billingReader and adds Tx-only mutations.
type billingWriter struct {
	billingReader
}

// InsertPartitionArchiveIfAbsent runs INSERT ... ON CONFLICT DO NOTHING
// RETURNING id. On conflict no row is returned; we then fetch the existing
// id via the natural key. The whole sequence executes inside the caller's Tx
// so the outbox row written by the application service stays atomic with the
// source-row write.
func (w *billingWriter) InsertPartitionArchiveIfAbsent(ctx context.Context, pa store.PartitionArchive) (uuid.UUID, bool, error) {
	const q = `
		INSERT INTO billing.partition_archives
		  (id, year, month, partition_name, lake_uri,
		   row_count, bytes_written, archived_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (year, month, partition_name) DO NOTHING
		RETURNING id`
	var id uuid.UUID
	err := w.q.QueryRow(ctx, q,
		pa.ID, pa.Year, pa.Month, pa.PartitionName, pa.LakeURI,
		pa.RowCount, pa.BytesWritten, pa.ArchivedAt,
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("postgres: InsertPartitionArchiveIfAbsent: %w", err)
	}
	// Conflict path: read the existing id by natural key.
	existing, gerr := w.GetPartitionArchiveByKey(ctx, pa.Year, pa.Month, pa.PartitionName)
	if gerr != nil {
		return uuid.Nil, false, gerr
	}
	if existing == nil {
		return uuid.Nil, false, fmt.Errorf("postgres: InsertPartitionArchiveIfAbsent: conflict but no row found for (%d,%d,%s)", pa.Year, pa.Month, pa.PartitionName)
	}
	return existing.ID, false, nil
}
