//go:build integration

package billing_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupPG(t *testing.T) *pgxpool.Pool {
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

	migrationsDir := filepath.Join("..", "..", "..", "..", "plane", "data", "migrations")
	for _, f := range []string{
		"000_init.sql", "001_identity.sql", "002_repositories.sql",
		"003_collaboration.sql", "004_ci.sql", "005_billing.sql",
	} {
		sql, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return pool
}

func partitionExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (
		   SELECT 1 FROM pg_class c
		   JOIN pg_namespace n ON n.oid = c.relnamespace
		   WHERE n.nspname = 'billing' AND c.relname = $1
		 )`, name,
	).Scan(&ok); err != nil {
		t.Fatalf("probe pg_class: %v", err)
	}
	return ok
}

func TestPostgresPartitioner_createsAndIsIdempotent(t *testing.T) {
	pool := setupPG(t)
	p := billing.NewPostgresPartitioner(pool)
	ctx := context.Background()

	created, err := p.CreateUsageEventsPartition(ctx, 2027, 5)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !created {
		t.Error("expected created=true on first call")
	}
	if !partitionExists(t, pool, "usage_events_2027_05") {
		t.Error("partition not present in pg_class")
	}

	created, err = p.CreateUsageEventsPartition(ctx, 2027, 5)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if created {
		t.Error("expected created=false on second call")
	}
}

func TestPostgresPartitioner_advisoryLockSerialisesConcurrentRetries(t *testing.T) {
	pool := setupPG(t)
	p := billing.NewPostgresPartitioner(pool)
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make([]struct {
		created bool
		err     error
	}, 5)
	for i := range results {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := p.CreateUsageEventsPartition(ctx, 2027, 6)
			results[i].created = created
			results[i].err = err
		}()
	}
	wg.Wait()

	createdCount := 0
	for i, r := range results {
		if r.err != nil {
			t.Errorf("call %d: %v", i, r.err)
		}
		if r.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Errorf("expected exactly one created=true under contention, got %d", createdCount)
	}
	if !partitionExists(t, pool, "usage_events_2027_06") {
		t.Error("partition missing after concurrent burst")
	}
}

func TestPostgresPartitioner_invalidArgs(t *testing.T) {
	pool := setupPG(t)
	p := billing.NewPostgresPartitioner(pool)
	ctx := context.Background()

	if _, err := p.CreateUsageEventsPartition(ctx, 2025, 1); err == nil {
		t.Error("expected error for year < 2026")
	}
	if _, err := p.CreateUsageEventsPartition(ctx, 2027, 13); err == nil {
		t.Error("expected error for month > 12")
	}
}
