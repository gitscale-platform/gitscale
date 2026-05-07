//go:build integration

package main_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	identityv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/identity/v1"
	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// setupPostgres starts a fresh PG container with the full identity migration
// chain applied. Returns a connected pool; cleanup terminates the container.
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

// startServer boots an in-process gRPC server backed by the postgres-backed
// identity.Service. Returns the listening address; cleanup gracefully stops
// the server.
func startServer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	store := storepg.New(pool)
	svc := identity.NewPostgresService(store)

	srv := grpc.NewServer()
	identityv1.RegisterIdentityServiceServer(srv, identity.NewGRPCServer(svc))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	return lis.Addr().String()
}

// TestGRPC_CreateAndDisableUser exercises the full path: gRPC client →
// generated stub → server adapter → identity.Service → postgres + outbox
// (ADR-008). DisableUser must produce an identity_outbox row of type
// "user.disabled" within the same Tx as the human_users update (verified via
// direct SQL after the call returns).
func TestGRPC_CreateAndDisableUser(t *testing.T) {
	pool := setupPostgres(t)
	addr := startServer(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })

	client := appclient.NewGRPCIdentityClient(cc)

	// CreateUser (direct stub call — appclient surface omits CreateUser).
	rawClient := identityv1.NewIdentityServiceClient(cc)
	createResp, err := rawClient.CreateUser(ctx, &identityv1.CreateUserRequest{
		Email:               "alice@example.com",
		PlaintextCredential: "S3cret!1234",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	userID, err := uuid.Parse(createResp.GetUser().GetId())
	if err != nil {
		t.Fatalf("parse created user id: %v", err)
	}

	// Round-trip GetUser.
	view, err := client.GetUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if view == nil || view.Email != "alice@example.com" {
		t.Fatalf("GetUser view: %+v", view)
	}

	// DisableUser via the workflow-plane appclient (production path).
	if err := client.DisableUser(ctx, userID, "spam"); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}

	// Verify outbox row landed (ADR-008 invariant).
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM identity_outbox
		   WHERE event_type = 'user.disabled' AND aggregate_id = $1`,
		userID,
	).Scan(&count); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 user.disabled outbox row for %s; got %d", userID, count)
	}
}
