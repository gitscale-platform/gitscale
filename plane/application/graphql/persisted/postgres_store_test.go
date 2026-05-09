//go:build integration

package persisted_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/persisted"
	"github.com/google/uuid"
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
		t.Fatalf("conn string: %v", err)
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	migrationsDir := filepath.Join("..", "..", "..", "..", "plane", "data", "migrations")
	mig, err := os.ReadFile(filepath.Join(migrationsDir, "011_graphql_persisted_queries.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return pool
}

func TestPostgresStore_PutGetRoundTrip(t *testing.T) {
	pool := setupPG(t)
	store := persisted.NewPostgresStore(pool)
	ctx := context.Background()

	hash := persisted.HashFor("{ user(login:\"a\") { id } }")
	if err := store.Put(ctx, hash, "{ user(login:\"a\") { id } }", uuid.New()); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.Get(ctx, hash)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "{ user(login:\"a\") { id } }" {
		t.Errorf("body mismatch: %q", got)
	}
}

func TestPostgresStore_GetNotFound(t *testing.T) {
	pool := setupPG(t)
	store := persisted.NewPostgresStore(pool)
	_, err := store.Get(context.Background(), "sha256:00")
	if !errors.Is(err, persisted.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestPostgresStore_PutIdempotentOnSameBody(t *testing.T) {
	pool := setupPG(t)
	store := persisted.NewPostgresStore(pool)
	ctx := context.Background()
	hash := persisted.HashFor("{ a }")
	if err := store.Put(ctx, hash, "{ a }", uuid.New()); err != nil {
		t.Fatalf("put1: %v", err)
	}
	if err := store.Put(ctx, hash, "{ a }", uuid.New()); err != nil {
		t.Fatalf("put2: %v", err)
	}
}

func TestPostgresStore_PutHashCollisionRejected(t *testing.T) {
	pool := setupPG(t)
	store := persisted.NewPostgresStore(pool)
	ctx := context.Background()
	// Force a hash collision by reusing a hash with a different body.
	hash := persisted.HashFor("{ a }")
	if err := store.Put(ctx, hash, "{ a }", uuid.New()); err != nil {
		t.Fatalf("put1: %v", err)
	}
	err := store.Put(ctx, hash, "{ b }", uuid.New())
	if !errors.Is(err, persisted.ErrHashConflict) {
		t.Fatalf("want ErrHashConflict, got %v", err)
	}
}
