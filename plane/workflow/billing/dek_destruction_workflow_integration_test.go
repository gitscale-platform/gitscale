//go:build integration

package billing

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	appbilling "github.com/gitscale-platform/gitscale/plane/application/billing"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	pgstore "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
	vault "github.com/hashicorp/vault/api"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// setupPostgresForDEK boots a postgres container with the migrations
// required for billing.partition_archives + billing.billing_outbox.
func setupPostgresForDEK(t *testing.T) *pgxpool.Pool {
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

// rotateTransitKey rotates the transit key once so it has version 2,
// leaving v1 as the version this test will destroy.
func rotateTransitKey(t *testing.T, ctx context.Context, c *vault.Client, mount, name string) {
	t.Helper()
	if _, err := c.Logical().WriteWithContext(ctx,
		mount+"/keys/"+name+"/rotate", map[string]any{}); err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	// Allow trim by enabling deletion_allowed.
	if _, err := c.Logical().WriteWithContext(ctx,
		mount+"/keys/"+name+"/config", map[string]any{
			"deletion_allowed": true,
		}); err != nil {
		t.Fatalf("config deletion_allowed: %v", err)
	}
}

// readMinAvailableVersion returns the key's min_available_version after the
// trim has settled. We poll briefly because trim is async-ish in dev mode.
func readMinAvailableVersion(t *testing.T, ctx context.Context, c *vault.Client, mount, name string) int {
	t.Helper()
	secret, err := c.Logical().ReadWithContext(ctx, mount+"/keys/"+name)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if secret == nil || secret.Data == nil {
		t.Fatalf("key %s/%s not found", mount, name)
	}
	return readIntFromSecret(secret.Data, "min_available_version")
}

// TestDEKDestructionWorkflow_Integration_VaultTrimAndOutbox boots Vault +
// Postgres testcontainers, seeds an eligible partition row, and asserts the
// workflow trims Vault transit key version 1 and writes the outbox row.
func TestDEKDestructionWorkflow_Integration_VaultTrimAndOutbox(t *testing.T) {
	ctx := context.Background()
	vaultClient := bootVaultForExport(t)
	rotateTransitKey(t, ctx, vaultClient, "transit", "platform-billing-master")

	pool := setupPostgresForDEK(t)
	ms := pgstore.New(pool)
	svc := appbilling.NewPostgresService(ms)
	billingClient := &localBillingClient{svc: svc}

	cutoff := time.Date(2034, 6, 1, 0, 0, 0, 0, time.UTC)
	archiveTime := cutoff.AddDate(0, 0, -10) // older than cutoff
	if err := ms.Transact(ctx, func(tx store.Tx) error {
		_, _, err := tx.Billing().InsertPartitionArchiveIfAbsent(ctx, store.PartitionArchive{
			Year: 2027, Month: 1, PartitionName: "billing.usage_events_2027_01",
			LakeURI: "s3://test/2027/01.parquet", RowCount: 1, BytesWritten: 1,
			ArchivedAt: archiveTime,
		})
		return err
	}); err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	// Wire activities.
	listAct, err := NewListEligiblePartitionsActivity(ms, &fixedKEKResolver{hint: "platform-billing-v1"})
	if err != nil {
		t.Fatalf("list activity: %v", err)
	}
	holdAct, _ := NewCheckLegalHoldActivity(NewStaticLegalHoldChecker(false, ""))
	approvalAct := NewRequestOperatorApprovalActivity(NewAutoApproveStub())
	destroy, err := NewDestroyDEKActivity(vaultClient, "transit", "platform-billing-master")
	if err != nil {
		t.Fatalf("destroy activity: %v", err)
	}
	emitAct, _ := NewEmitDEKDestroyedActivity(billingClient)

	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(DEKDestructionWorkflow)
	env.RegisterActivityWithOptions(listAct.Execute, activity.RegisterOptions{Name: ActivityNameListEligiblePartitions})
	env.RegisterActivityWithOptions(holdAct.Execute, activity.RegisterOptions{Name: ActivityNameCheckLegalHold})
	env.RegisterActivityWithOptions(approvalAct.Execute, activity.RegisterOptions{Name: ActivityNameRequestOperatorApproval})
	env.RegisterActivityWithOptions(destroy.Execute, activity.RegisterOptions{Name: ActivityNameDestroyDEK})
	env.RegisterActivityWithOptions(emitAct.Execute, activity.RegisterOptions{Name: ActivityNameEmitDEKDestroyed})

	env.ExecuteWorkflow(DEKDestructionWorkflow, DEKDestructionInput{
		RunTime: cutoff,
		Cutoff:  cutoff,
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result DEKDestructionResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if result.KeysDestroyed != 1 {
		t.Fatalf("KeysDestroyed=%d skipped=%v", result.KeysDestroyed, result.Skipped)
	}

	// Vault assertion: v1 trimmed → min_available_version >= 2.
	if got := readMinAvailableVersion(t, ctx, vaultClient, "transit", "platform-billing-master"); got < 2 {
		t.Fatalf("min_available_version=%d want >= 2", got)
	}

	// Outbox assertion: exactly one billing.partition_dek_destroyed row.
	var outboxCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.billing_outbox WHERE event_type = $1`,
		appbilling.EventTypePartitionDEKDestroyed,
	).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("outbox count=%d want 1", outboxCount)
	}

	// Payload sanity-check.
	var rawPayload []byte
	if err := pool.QueryRow(ctx,
		`SELECT payload::text FROM billing.billing_outbox WHERE event_type = $1`,
		appbilling.EventTypePartitionDEKDestroyed,
	).Scan(&rawPayload); err != nil {
		t.Fatalf("scan payload: %v", err)
	}
	var p appbilling.PartitionDEKDestroyedPayload
	if err := json.Unmarshal(rawPayload, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.KEKHint != "platform-billing-v1" || p.VaultKeyVersion != 1 {
		t.Errorf("payload kek_hint=%q version=%d", p.KEKHint, p.VaultKeyVersion)
	}
}

// fixedKEKResolver is a tiny resolver that returns the same hint for every
// lake URI. The test path doesn't exercise real S3.
type fixedKEKResolver struct{ hint string }

func (r *fixedKEKResolver) ResolveKEKHint(_ context.Context, _ string) (string, error) {
	return r.hint, nil
}

// localBillingClient implements appclient.BillingClient by calling the in-
// process service directly. Avoids spinning a gRPC server for the test.
type localBillingClient struct{ svc *appbilling.PostgresService }

func (l *localBillingClient) RecordPartitionArchived(ctx context.Context, in appclient.PartitionArchivedInput) error {
	_, err := l.svc.RecordPartitionArchived(ctx, appbilling.RecordPartitionArchivedInput{
		Year: in.Year, Month: in.Month, PartitionName: in.PartitionName,
		LakeURI: in.LakeURI, RowCount: in.RowCount, BytesWritten: in.BytesWritten,
	})
	return err
}

func (l *localBillingClient) RecordDEKDestroyed(ctx context.Context, in appclient.DEKDestroyedInput) error {
	_, err := l.svc.RecordDEKDestroyed(ctx, appbilling.RecordDEKDestroyedInput{
		Year: in.Year, Month: in.Month, PartitionName: in.PartitionName,
		KEKHint: in.KEKHint, VaultKeyVersion: in.VaultKeyVersion,
	})
	return err
}

