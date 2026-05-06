//go:build integration

package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/compliance"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	pgstore "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestPostgresMetadataStoreCompliance runs the ADR-017 contract suite against
// the postgres MetadataStore. Each subtest gets its own container to avoid
// cross-test state leakage in the outbox tables.
func TestPostgresMetadataStoreCompliance(t *testing.T) {
	factory := func(t *testing.T) (store.MetadataStore, compliance.OutboxVerifier, func()) {
		ctx := context.Background()
		ctr, err := pgmodule.Run(ctx,
			"postgres:16-alpine",
			pgmodule.WithDatabase("gitscale_test"),
			pgmodule.WithUsername("gs"),
			pgmodule.WithPassword("gs"),
			testcontainers.WithWaitStrategy(pgmodule.WithWaitForReadiness()),
		)
		if err != nil {
			t.Fatalf("start postgres container: %v", err)
		}
		connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatalf("connection string: %v", err)
		}
		pool, err := pgxpool.New(ctx, connStr)
		if err != nil {
			t.Fatalf("pgxpool.New: %v", err)
		}

		// Run migrations 000-005 against the fresh DB.
		migrationsDir := filepath.Join("..", "..", "migrations")
		for _, f := range []string{
			"000_init.sql", "001_identity.sql", "002_repositories.sql",
			"003_collaboration.sql", "004_ci.sql", "005_billing.sql",
		} {
			sql, err := os.ReadFile(filepath.Join(migrationsDir, f))
			if err != nil {
				t.Fatalf("read migration %s: %v", f, err)
			}
			if _, err := pool.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("apply migration %s: %v", f, err)
			}
		}

		s := pgstore.New(pool)
		v := &pgVerifier{pool: pool}
		cleanup := func() {
			pool.Close()
			_ = ctr.Terminate(ctx)
		}
		return s, v, cleanup
	}
	compliance.RunMetadataStoreCompliance(t, factory, compliance.MetadataStoreOptions{})
}

// pgVerifier reads outbox rows directly from the per-domain tables for the
// compliance suite assertions.
type pgVerifier struct {
	pool *pgxpool.Pool
}

func (v *pgVerifier) OutboxCount(ctx context.Context, domain store.Domain, eventType string) (int, error) {
	q := `SELECT COUNT(*) FROM ` + domain.OutboxTable() + ` WHERE event_type = $1`
	var count int
	err := v.pool.QueryRow(ctx, q, eventType).Scan(&count)
	return count, err
}

func (v *pgVerifier) OutboxEventIDs(ctx context.Context, domain store.Domain) ([]uuid.UUID, error) {
	q := `SELECT event_id FROM ` + domain.OutboxTable() + ` ORDER BY id`
	rows, err := v.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
