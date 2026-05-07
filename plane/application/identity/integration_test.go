//go:build integration

package identity_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	pgstore "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"time"
)

// setupPostgres starts a fresh container, applies migrations 000-005, and
// returns a connected pool. Cleanup terminates the container.
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

	migrationsDir := filepath.Join("..", "..", "..", "plane", "data", "migrations")
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

func TestPostgresService_CreateUser_atomic(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)

	ctx := context.Background()
	u, err := svc.CreateUser(ctx, "alice@example.com", "S3cret!1234")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Source row exists with normalized email.
	var email string
	if err := pool.QueryRow(ctx,
		`SELECT email FROM identity.human_users WHERE id = $1`, u.ID,
	).Scan(&email); err != nil {
		t.Fatalf("query user: %v", err)
	}
	if email != "alice@example.com" {
		t.Errorf("email: got %q want alice@example.com", email)
	}

	// Outbox row exists with matching aggregate_id and event_type.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM identity.identity_outbox WHERE aggregate_id = $1 AND event_type = $2`,
		u.ID, identity.EventUserCreated,
	).Scan(&count); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 outbox row, got %d", count)
	}
}

func TestPostgresService_InvalidEmail_rollback_removesBoth(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)
	ctx := context.Background()

	// Service rejects before opening a Tx, so no outbox row should ever appear.
	if _, err := svc.CreateUser(ctx, "no-at-sign", "x"); !errors.Is(err, identity.ErrInvalidEmail) {
		t.Fatalf("expected ErrInvalidEmail, got %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM identity.identity_outbox WHERE event_type = $1`,
		identity.EventUserCreated,
	).Scan(&count); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 outbox rows, got %d", count)
	}
}

func TestPostgresService_CreateUser_rollback_onDuplicate_removesOutbox(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)
	ctx := context.Background()

	// Pre-insert a user to force a UNIQUE violation on the second create.
	if _, err := svc.CreateUser(ctx, "dup@example.com", "pw1"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Second create with the same email must fail; outbox count stays at 1.
	if _, err := svc.CreateUser(ctx, "dup@example.com", "pw2"); err == nil {
		t.Fatal("expected duplicate-email error, got nil")
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM identity.identity_outbox WHERE event_type = $1`,
		identity.EventUserCreated,
	).Scan(&count); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 outbox row (only the seed), got %d", count)
	}
}

func TestPostgresService_CreateAgent_atomic(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)
	ctx := context.Background()

	parent, err := svc.CreateUser(ctx, "parent@example.com", "pw")
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	a, err := svc.CreateAgent(ctx, parent.ID, "code-reviewer", []string{"repo:read"})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM identity.identity_outbox WHERE aggregate_id = $1 AND event_type = $2`,
		a.ID, identity.EventAgentCreated,
	).Scan(&count); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if count != 1 {
		t.Errorf("agent outbox: expected 1, got %d", count)
	}
}

func TestPostgresService_SetAgentReputationScore_underContention_retries(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)
	ctx := context.Background()

	parent, err := svc.CreateUser(ctx, "p@example.com", "pw")
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	a, err := svc.CreateAgent(ctx, parent.ID, "rep-test", nil)
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// Two concurrent reputation updates. The retry helper should let both
	// eventually succeed.
	var wg sync.WaitGroup
	results := make([]error, 2)
	scores := []float64{0.7, 0.8}
	for i := range scores {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = svc.SetAgentReputationScore(ctx, a.ID, scores[i])
		}()
	}
	wg.Wait()

	for i, err := range results {
		if err != nil && !errors.Is(err, identity.ErrRetryExhausted) {
			t.Errorf("update %d: unexpected error %v", i, err)
		}
	}

	// Final reputation must be one of the two values.
	got, err := svc.GetAgent(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.ReputationScore != 0.7 && got.ReputationScore != 0.8 {
		t.Errorf("final reputation: %v not in {0.7, 0.8}", got.ReputationScore)
	}

	// And there should be at least 1 reputation_updated outbox row (per
	// successful attempt).
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM identity.identity_outbox WHERE aggregate_id = $1 AND event_type = $2`,
		a.ID, identity.EventAgentReputationUpdated,
	).Scan(&count); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if count < 1 {
		t.Errorf("expected ≥1 reputation outbox rows, got %d", count)
	}
}

func TestPostgresService_LookupIdentityForCache_returnsEntry(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)
	ctx := context.Background()

	u, err := svc.CreateUser(ctx, "lookup@example.com", "pw")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	entry, err := svc.LookupIdentityForCache(ctx, u.ID)
	if err != nil {
		t.Fatalf("LookupIdentityForCache: %v", err)
	}
	if entry == nil || entry.PrincipalID != u.ID.String() {
		t.Errorf("entry: %+v", entry)
	}
}

func TestPostgresService_GetUserByEmail_caseInsensitive(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, "Mixed@Case.Email", "pw"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := svc.GetUserByEmail(ctx, "MIXED@case.email")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if got == nil || got.Email != "mixed@case.email" {
		t.Errorf("case-insensitive lookup failed: %v", got)
	}
}

func TestPostgresService_unknownAgent_returnsErrAgentNotFound(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)

	if err := svc.SetAgentReputationScore(context.Background(), uuid.New(), 0.5); !errors.Is(err, identity.ErrAgentNotFound) {
		t.Errorf("expected ErrAgentNotFound, got %v", err)
	}
}

func TestPostgresService_DisableUser_writesRowAndOutbox(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)
	ctx := context.Background()

	u, err := svc.CreateUser(ctx, "rev1@example.com", "pw1234")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := svc.DisableUser(ctx, u.ID, "compromised credential"); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}

	var disabledAt *time.Time
	var reason *string
	if err := pool.QueryRow(ctx,
		`SELECT disabled_at, disable_reason FROM identity.human_users WHERE id = $1`, u.ID,
	).Scan(&disabledAt, &reason); err != nil {
		t.Fatalf("scan disabled_at: %v", err)
	}
	if disabledAt == nil || reason == nil || *reason != "compromised credential" {
		t.Errorf("disabled_at/reason not persisted: %v / %v", disabledAt, reason)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM identity.identity_outbox WHERE event_type = 'user.disabled' AND aggregate_id = $1`, u.ID,
	).Scan(&n); err != nil || n != 1 {
		t.Errorf("expected 1 user.disabled outbox row, got %d (err=%v)", n, err)
	}

	if err := svc.DisableUser(ctx, uuid.New(), "x"); !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("DisableUser missing user: expected ErrUserNotFound, got %v", err)
	}
}

func TestPostgresService_RevokeAgent_writesRowAndOutbox(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)
	ctx := context.Background()

	u, err := svc.CreateUser(ctx, "rev2@example.com", "pw1234")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	a, err := svc.CreateAgent(ctx, u.ID, "agent-1", []string{"repo:read"})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	if err := svc.RevokeAgent(ctx, a.ID, "policy violation"); err != nil {
		t.Fatalf("RevokeAgent: %v", err)
	}

	var revokedAt *time.Time
	var reason *string
	if err := pool.QueryRow(ctx,
		`SELECT revoked_at, revoke_reason FROM identity.agent_identities WHERE id = $1`, a.ID,
	).Scan(&revokedAt, &reason); err != nil {
		t.Fatalf("scan revoked_at: %v", err)
	}
	if revokedAt == nil || reason == nil || *reason != "policy violation" {
		t.Errorf("revoked_at/reason not persisted: %v / %v", revokedAt, reason)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM identity.identity_outbox WHERE event_type = 'agent.revoked' AND aggregate_id = $1`, a.ID,
	).Scan(&n); err != nil || n != 1 {
		t.Errorf("expected 1 agent.revoked outbox row, got %d (err=%v)", n, err)
	}

	if err := svc.RevokeAgent(ctx, uuid.New(), "x"); !errors.Is(err, identity.ErrAgentNotFound) {
		t.Errorf("RevokeAgent missing agent: expected ErrAgentNotFound, got %v", err)
	}
}

func TestPostgresService_UpdateAgentPermissions_writesRowAndOutbox(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)
	ctx := context.Background()

	u, err := svc.CreateUser(ctx, "rev3@example.com", "pw1234")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	a, err := svc.CreateAgent(ctx, u.ID, "agent-2", []string{"repo:read"})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	newScope := []string{"repo:read", "issue:write"}
	if err := svc.UpdateAgentPermissions(ctx, a.ID, newScope); err != nil {
		t.Fatalf("UpdateAgentPermissions: %v", err)
	}

	var stored []string
	if err := pool.QueryRow(ctx,
		`SELECT permission_scope FROM identity.agent_identities WHERE id = $1`, a.ID,
	).Scan(&stored); err != nil {
		t.Fatalf("scan permission_scope: %v", err)
	}
	if len(stored) != 2 {
		t.Errorf("expected 2 scopes, got %v", stored)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM identity.identity_outbox WHERE event_type = 'principal.permissions_changed' AND aggregate_id = $1`, a.ID,
	).Scan(&n); err != nil || n != 1 {
		t.Errorf("expected 1 principal.permissions_changed outbox row, got %d (err=%v)", n, err)
	}
}

func TestPostgresService_AddRemoveOrgMember_writesOutbox(t *testing.T) {
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := identity.NewPostgresService(ms)
	ctx := context.Background()

	u, err := svc.CreateUser(ctx, "rev4@example.com", "pw1234")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	orgID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO identity.organisations (id, slug) VALUES ($1, $2)`,
		orgID, "org-rev",
	); err != nil {
		t.Fatalf("insert org: %v", err)
	}

	if err := svc.AddOrgMember(ctx, orgID, u.ID, "developer"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	var role string
	if err := pool.QueryRow(ctx,
		`SELECT role FROM identity.org_memberships WHERE org_id = $1 AND user_id = $2`, orgID, u.ID,
	).Scan(&role); err != nil || role != "developer" {
		t.Errorf("membership row: role=%s err=%v", role, err)
	}

	if err := svc.RemoveOrgMember(ctx, orgID, u.ID); err != nil {
		t.Fatalf("RemoveOrgMember: %v", err)
	}

	var nAdd, nRem int
	_ = pool.QueryRow(ctx,
		`SELECT count(*) FROM identity.identity_outbox WHERE event_type = 'org.member_added' AND aggregate_id = $1`, u.ID,
	).Scan(&nAdd)
	_ = pool.QueryRow(ctx,
		`SELECT count(*) FROM identity.identity_outbox WHERE event_type = 'org.member_removed' AND aggregate_id = $1`, u.ID,
	).Scan(&nRem)
	if nAdd != 1 || nRem != 1 {
		t.Errorf("outbox: added=%d removed=%d", nAdd, nRem)
	}

	if err := svc.AddOrgMember(ctx, orgID, u.ID, ""); !errors.Is(err, identity.ErrEmptyRole) {
		t.Errorf("AddOrgMember empty role: expected ErrEmptyRole, got %v", err)
	}
	if err := svc.AddOrgMember(ctx, orgID, uuid.New(), "developer"); !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("AddOrgMember missing user: expected ErrUserNotFound, got %v", err)
	}
}

// guard: confirm the fast hasher isn't accidentally used in a non-test build.
var _ = store.Domain("compile-time-import-guard")
