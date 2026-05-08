//go:build integration

package main_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"github.com/gitscale-platform/gitscale/plane/application/billing"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// setupPostgres mirrors cmd/identity-service/integration_test.go with the
// 007 migration applied so billing.partition_archives exists.
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

	migrationsDir := filepath.Join("..", "..", "plane", "data", "migrations")
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

// startServer spins up the same gRPC stack the binary builds in main.go,
// against the supplied pool. Returned address is dial-ready.
func startServer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	store := storepg.New(pool)
	svc := billing.NewPostgresService(store)

	srv := grpc.NewServer()
	billingv1.RegisterBillingServiceServer(srv, billing.NewGRPCServer(svc))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	return lis.Addr().String()
}

// TestGRPC_RecordPartitionArchived_FullStack exercises the full server path
// over a real TCP listener — boot, dial, RecordPartitionArchived, then verify
// outbox row landed (ADR-008 invariant).
func TestGRPC_RecordPartitionArchived_FullStack(t *testing.T) {
	pool := setupPostgres(t)
	addr := startServer(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })

	client := billingv1.NewBillingServiceClient(cc)

	resp, err := client.RecordPartitionArchived(ctx, &billingv1.RecordPartitionArchivedRequest{
		Year:          2026,
		Month:         5,
		PartitionName: "usage_events_2026_05",
		LakeUri:       "s3://lake/billing/usage_events/2026/05/",
		RowCount:      1000,
		BytesWritten:  131072,
	})
	if err != nil {
		t.Fatalf("RecordPartitionArchived: %v", err)
	}
	if !resp.GetCreated() {
		t.Fatalf("expected created=true on first call")
	}
	if resp.GetArchiveId() == "" {
		t.Fatalf("expected non-empty archive_id")
	}

	var outboxCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.billing_outbox WHERE event_type = $1 AND aggregate_id::text = $2`,
		billing.EventTypePartitionArchived, resp.GetArchiveId(),
	).Scan(&outboxCount); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("expected 1 outbox row for archive_id %s, got %d", resp.GetArchiveId(), outboxCount)
	}
}
