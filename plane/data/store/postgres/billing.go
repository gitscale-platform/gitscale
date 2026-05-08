package postgres

import (
	"context"
	"errors"
	"fmt"

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
