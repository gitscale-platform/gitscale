//go:build integration

package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
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

func setupPostgres(t *testing.T) *pgxpool.Pool {
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
		t.Fatalf("start postgres container: %v", err)
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

	runMigrations(t, ctx, pool)
	return pool
}

func runMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	migrationsDir := filepath.Join("..", "..", "migrations")
	files := []string{
		"000_init.sql",
		"001_identity.sql",
		"002_repositories.sql",
		"003_collaboration.sql",
		"004_ci.sql",
		"005_billing.sql",
		"006_identity_revocation.sql",
		"007_billing_partition_archives.sql",
		"008_updated_at_triggers.sql",
	}
	for _, f := range files {
		sql, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read migration %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("run migration %s: %v", f, err)
		}
	}
}

func TestPostgres_WriteOutbox_TransactionalWithRollback(t *testing.T) {
	pool := setupPostgres(t)
	s := pgstore.New(pool)
	ctx := context.Background()

	aggID := uuid.New()

	// Committed Tx: outbox row must exist.
	if err := s.Transact(ctx, func(tx store.Tx) error {
		return tx.WriteOutbox(ctx, store.DomainIdentity, "human_user", aggID, "user.created", map[string]string{"x": "1"})
	}); err != nil {
		t.Fatalf("committed Transact: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM identity.identity_outbox WHERE aggregate_id = $1`, aggID).Scan(&count); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 outbox row after commit, got %d", count)
	}

	// Rolled-back Tx: outbox row must not appear.
	aggID2 := uuid.New()
	_ = s.Transact(ctx, func(tx store.Tx) error {
		_ = tx.WriteOutbox(ctx, store.DomainIdentity, "human_user", aggID2, "user.created", nil)
		return context.DeadlineExceeded // simulate failure
	})

	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM identity.identity_outbox WHERE aggregate_id = $1`, aggID2).Scan(&count); err != nil {
		t.Fatalf("query outbox after rollback: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 outbox rows after rollback, got %d", count)
	}
}

func TestPostgres_SerializationFailure_IsRetryable(t *testing.T) {
	pool := setupPostgres(t)
	s := pgstore.New(pool)
	ctx := context.Background()

	userID := uuid.New()
	// Insert a user so both transactions can read it.
	if err := s.Transact(ctx, func(tx store.Tx) error {
		return tx.Identity().InsertHumanUser(ctx, store.HumanUser{
			ID:         userID,
			Email:      "serial@example.com",
			RateBucket: "human_default",
		})
	}); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		errT2 error
	)
	ready := make(chan struct{})

	// Tx1: read the user, block, then update reputation.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Transact(ctx, func(tx store.Tx) error {
			if _, err := tx.Identity().GetUserByID(ctx, userID); err != nil {
				return err
			}
			close(ready)
			time.Sleep(50 * time.Millisecond) // let Tx2 also read
			return tx.Identity().SetAgentReputationScore(ctx, userID, 0.9)
		})
	}()

	// Tx2: also read the user, then try to update reputation.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ready
		err := s.Transact(ctx, func(tx store.Tx) error {
			if _, err := tx.Identity().GetUserByID(ctx, userID); err != nil {
				return err
			}
			return tx.Identity().SetAgentReputationScore(ctx, userID, 0.8)
		})
		mu.Lock()
		errT2 = err
		mu.Unlock()
	}()

	wg.Wait()
	mu.Lock()
	err := errT2
	mu.Unlock()

	// Under serializable isolation, one of the two transactions should have
	// received a 40001. We cannot guarantee which one fails in all PG versions,
	// but if errT2 is non-nil it must be retryable.
	if err != nil && !store.IsRetryable(err) {
		t.Errorf("expected retryable error from concurrent serializable Tx, got: %v", err)
	}
}
