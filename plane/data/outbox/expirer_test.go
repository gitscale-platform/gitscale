//go:build integration

package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// insertProcessedRow seeds an identity_outbox row with a chosen processed_at.
// processedAt nil leaves the row unprocessed (must never be deleted).
func insertProcessedRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, processedAt *time.Time) {
	t.Helper()
	_, err := pool.Exec(ctx, `INSERT INTO identity.identity_outbox
        (event_id, aggregate_type, aggregate_id, event_type, payload, processed_at)
        VALUES ($1, 'user', $2, 'user.created', '{}'::jsonb, $3)`,
		uuid.New(), uuid.New(), processedAt)
	if err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}
}

func TestExpirer_DeletesOnlyOldProcessedRows(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — requires Docker")
	}
	ctx := context.Background()
	pool, cleanup := testDB(t)
	defer cleanup()

	old := time.Now().UTC().Add(-25 * time.Hour)
	recent := time.Now().UTC().Add(-1 * time.Hour)
	insertProcessedRow(t, ctx, pool, &old)    // candidate for deletion
	insertProcessedRow(t, ctx, pool, &recent) // too recent
	insertProcessedRow(t, ctx, pool, nil)     // unprocessed — must never be deleted

	exp := outbox.NewExpirer(pool, store.DomainIdentity, outbox.ExpirerOptions{
		TTL:       24 * time.Hour,
		BatchSize: 1000,
		Registry:  prometheus.NewRegistry(),
	})
	deleted, err := exp.Expire(ctx)
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d want 1", deleted)
	}

	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.identity_outbox`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 2 {
		t.Fatalf("remaining=%d want 2", remaining)
	}

	// Re-running yields zero deletions: idempotent no-op.
	deleted, err = exp.Expire(ctx)
	if err != nil {
		t.Fatalf("Expire (idempotent): %v", err)
	}
	if deleted != 0 {
		t.Fatalf("idempotent re-run deleted=%d want 0", deleted)
	}
}

func TestExpirer_BatchedToCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — requires Docker")
	}
	ctx := context.Background()
	pool, cleanup := testDB(t)
	defer cleanup()

	// Seed enough rows to require multiple batches at BatchSize=10k.
	const total = 25_000
	old := time.Now().UTC().Add(-48 * time.Hour)
	// Bulk insert via a single Postgres statement: generate_series.
	if _, err := pool.Exec(ctx, `
        INSERT INTO identity.identity_outbox
            (event_id, aggregate_type, aggregate_id, event_type, payload, processed_at)
        SELECT gen_random_uuid(), 'user', gen_random_uuid(), 'user.created', '{}'::jsonb, $1
        FROM generate_series(1, $2)`, old, total); err != nil {
		t.Fatalf("bulk insert: %v", err)
	}

	exp := outbox.NewExpirer(pool, store.DomainIdentity, outbox.ExpirerOptions{
		BatchSize: 10000,
	})
	deleted, err := exp.Expire(ctx)
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if deleted != int64(total) {
		t.Fatalf("deleted=%d want %d", deleted, total)
	}

	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.identity_outbox`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining=%d want 0", remaining)
	}
}

