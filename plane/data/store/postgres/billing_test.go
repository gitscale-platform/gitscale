//go:build integration

package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	pgstore "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// setupBillingPool boots a fresh testcontainer with the full migration chain
// applied, including 007_billing_partition_archives.sql. Returned pool is
// closed and the container terminated by t.Cleanup.
func setupBillingPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	ctr, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("gitscale_test"),
		pgmodule.WithUsername("gs"),
		pgmodule.WithPassword("gs"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	migrationsDir := filepath.Join("..", "..", "migrations")
	for _, f := range []string{
		"000_init.sql", "001_identity.sql", "002_repositories.sql",
		"003_collaboration.sql", "004_ci.sql", "005_billing.sql",
		"006_identity_revocation.sql",
		"007_billing_partition_archives.sql",
	} {
		sql, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read migration %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply migration %s: %v", f, err)
		}
	}
	return pool
}

func TestInsertPartitionArchiveIfAbsent_FirstAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := setupBillingPool(t)
	ms := pgstore.New(pool)

	pa := store.PartitionArchive{
		ID:            uuid.New(),
		Year:          2026,
		Month:         5,
		PartitionName: "usage_events_2026_05",
		LakeURI:       "s3://lake/billing/usage_events/2026/05/",
		RowCount:      100,
		BytesWritten:  1024,
		ArchivedAt:    time.Now().UTC(),
	}

	var firstID uuid.UUID
	if err := ms.Transact(ctx, func(tx store.Tx) error {
		id, created, err := tx.Billing().InsertPartitionArchiveIfAbsent(ctx, pa)
		if err != nil {
			return err
		}
		if !created {
			t.Fatalf("expected created=true on first insert")
		}
		firstID = id
		return nil
	}); err != nil {
		t.Fatalf("first tx: %v", err)
	}
	if firstID == uuid.Nil {
		t.Fatalf("expected non-nil id from first insert")
	}

	// Idempotent retry — different attempt UUID and lake URI, same natural key.
	pa2 := pa
	pa2.ID = uuid.New()
	pa2.LakeURI = "s3://lake/billing/usage_events/2026/05/RETRY"
	if err := ms.Transact(ctx, func(tx store.Tx) error {
		id, created, err := tx.Billing().InsertPartitionArchiveIfAbsent(ctx, pa2)
		if err != nil {
			return err
		}
		if created {
			t.Fatalf("expected created=false on idempotent retry")
		}
		if id != firstID {
			t.Fatalf("expected returned id %s, got %s", firstID, id)
		}
		return nil
	}); err != nil {
		t.Fatalf("retry tx: %v", err)
	}

	// Source row count remains 1; the retry must not have produced a duplicate.
	var rowCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.partition_archives WHERE partition_name = $1`,
		pa.PartitionName,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected 1 partition_archives row after retry, got %d", rowCount)
	}

	// Lake URI must be the original one — ON CONFLICT DO NOTHING does not overwrite.
	var lakeURI string
	if err := pool.QueryRow(ctx,
		`SELECT lake_uri FROM billing.partition_archives WHERE id = $1`, firstID,
	).Scan(&lakeURI); err != nil {
		t.Fatalf("scan lake_uri: %v", err)
	}
	if lakeURI != pa.LakeURI {
		t.Fatalf("lake_uri overwritten: got %q want %q", lakeURI, pa.LakeURI)
	}
}

func TestGetPartitionArchiveByKey_NotFoundReturnsNilNil(t *testing.T) {
	ctx := context.Background()
	pool := setupBillingPool(t)
	ms := pgstore.New(pool)

	got, err := ms.Billing().GetPartitionArchiveByKey(ctx, 2026, 5, "missing_partition")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil row, got %+v", got)
	}
}
