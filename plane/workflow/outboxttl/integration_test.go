//go:build integration

package outboxttl_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/workflow/outboxttl"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// setupPostgres mirrors the helper in plane/data/store/postgres/postgres_test.go.
// We duplicate rather than import because Go's *_test.go files do not export
// symbols across packages.
func setupPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("gitscale_test"),
		tcpostgres.WithUsername("gs"),
		tcpostgres.WithPassword("gs"),
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
	root := repoRoot(t)
	files := []string{
		"000_init.sql",
		"001_identity.sql",
		"002_repositories.sql",
		"003_collaboration.sql",
		"004_ci.sql",
		"005_billing.sql",
	}
	for _, f := range files {
		sql, err := os.ReadFile(filepath.Join(root, "plane", "data", "migrations", f))
		if err != nil {
			t.Fatalf("read migration %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("run migration %s: %v", f, err)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	parts := strings.Split(dir, string(os.PathSeparator))
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "gitscale" || strings.HasPrefix(parts[i], "feat-workflow-outbox-ttl") {
			return string(os.PathSeparator) + filepath.Join(parts[:i+1]...)
		}
	}
	t.Fatalf("could not find repo root from %s", dir)
	return ""
}

// TestIntegration_ExpireOutboxesWorkflow_DrainsAcrossDomains seeds expired
// rows in two domains and runs the real workflow + activity wired to a real
// Expirer. Asserts each domain's RowsDeleted matches expectation.
func TestIntegration_ExpireOutboxesWorkflow_DrainsAcrossDomains(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — requires Docker")
	}
	ctx := context.Background()
	pool := setupPostgres(t)

	old := time.Now().UTC().Add(-48 * time.Hour)
	recent := time.Now().UTC().Add(-1 * time.Hour)

	// identity_outbox: 3 expired, 1 fresh, 1 unprocessed.
	for i := 0; i < 3; i++ {
		mustInsert(t, ctx, pool, "identity.identity_outbox", &old)
	}
	mustInsert(t, ctx, pool, "identity.identity_outbox", &recent)
	mustInsert(t, ctx, pool, "identity.identity_outbox", nil)

	// repositories_outbox: 2 expired.
	for i := 0; i < 2; i++ {
		mustInsert(t, ctx, pool, "repositories.repositories_outbox", &old)
	}

	// Other domains: empty (zero deletions expected).

	expirers := map[store.Domain]*outbox.Expirer{
		store.DomainIdentity:      outbox.NewExpirer(pool, store.DomainIdentity, outbox.ExpirerOptions{BatchSize: 1000}),
		store.DomainRepositories:  outbox.NewExpirer(pool, store.DomainRepositories, outbox.ExpirerOptions{BatchSize: 1000}),
		store.DomainCollaboration: outbox.NewExpirer(pool, store.DomainCollaboration, outbox.ExpirerOptions{BatchSize: 1000}),
		store.DomainCI:            outbox.NewExpirer(pool, store.DomainCI, outbox.ExpirerOptions{BatchSize: 1000}),
		store.DomainBilling:       outbox.NewExpirer(pool, store.DomainBilling, outbox.ExpirerOptions{BatchSize: 1000}),
	}
	act := outboxttl.NewExpireDomainOutboxActivity(expirers)

	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(outboxttl.ExpireOutboxesWorkflow)
	env.RegisterActivityWithOptions(act.Execute, activity.RegisterOptions{
		Name: outboxttl.ActivityNameExpireDomainOutbox,
	})

	env.ExecuteWorkflow(outboxttl.ExpireOutboxesWorkflow)
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	var got outboxttl.ExpireOutboxesResult
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if len(got.PerDomain) != 5 {
		t.Fatalf("expected 5 domain results, got %d", len(got.PerDomain))
	}

	want := map[string]int64{
		string(store.DomainIdentity):      3,
		string(store.DomainRepositories):  2,
		string(store.DomainCollaboration): 0,
		string(store.DomainCI):            0,
		string(store.DomainBilling):       0,
	}
	for _, r := range got.PerDomain {
		if w, ok := want[r.Domain]; !ok || r.RowsDeleted != w {
			t.Errorf("domain=%s RowsDeleted=%d want %d", r.Domain, r.RowsDeleted, w)
		}
	}

	// DB-level verification: identity has 2 rows remaining (1 fresh + 1 unprocessed).
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.identity_outbox`).Scan(&n); err != nil {
		t.Fatalf("count identity: %v", err)
	}
	if n != 2 {
		t.Errorf("identity remaining=%d want 2", n)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM repositories.repositories_outbox`).Scan(&n); err != nil {
		t.Fatalf("count repositories: %v", err)
	}
	if n != 0 {
		t.Errorf("repositories remaining=%d want 0", n)
	}
}

func mustInsert(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, processedAt *time.Time) {
	t.Helper()
	//nolint:gosec // table comes from typed test code
	_, err := pool.Exec(ctx, "INSERT INTO "+table+
		` (event_id, aggregate_type, aggregate_id, event_type, payload, processed_at)
          VALUES ($1, 'agg', $2, 'evt.created', '{}'::jsonb, $3)`,
		uuid.New(), uuid.New(), processedAt)
	if err != nil {
		t.Fatalf("insert %s: %v", table, err)
	}
}
