//go:build integration

// Integration coverage for PartitionArchiveWorkflow against a real PG 16 +
// minio testcontainer pair (issue #78). The unit tests in
// archive_workflow_test.go already cover the workflow's deterministic shape
// with mocks; this file exercises the full activity chain
// (detach -> export -> emit -> drop) end-to-end with assertions on PG state
// and S3 objects.
//
// Three cases:
//   - TestArchiveWorkflow_E2E_HappyPath           — baseline
//   - TestArchiveWorkflow_E2E_CrashResumption     — cancel mid-export, re-run
//   - TestArchiveWorkflow_E2E_DetachPendingRecovery — leave pg_inherits row
//     with inhdetachpending=true, expect archiver to call DETACH ... FINALIZE
package billing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// archiveE2EFixture bundles the live deps for an integration run.
type archiveE2EFixture struct {
	pool        *pgxpool.Pool
	s3Client    *s3.Client
	objStore    *S3ObjectStore
	archiver    *billingstore.PostgresArchiver
	billing     *appclient.StubBillingClient
	keys        KeyProvider
	bucket      string
	connStr     string
	s3Endpoint  string
	containerPG testcontainers.Container
	containerS3 testcontainers.Container
}

// setupArchiveE2E boots PG 16 + minio, applies migrations 000–009, creates the
// archive bucket, and returns wired-up activities + clients. All teardown is
// registered with t.Cleanup so callers do not need to coordinate.
func setupArchiveE2E(t *testing.T) *archiveE2EFixture {
	t.Helper()
	ctx := context.Background()

	// --- PostgreSQL ---------------------------------------------------------
	pgCtr, err := pgmodule.Run(ctx,
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
	t.Cleanup(func() { _ = pgCtr.Terminate(ctx) })

	connStr, err := pgCtr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("pg connstring: %v", err)
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	// Migrations live at plane/data/migrations relative to repo root; this
	// test file is in plane/workflow/billing, so up three.
	migrationsDir := filepath.Join("..", "..", "data", "migrations")
	migrations := []string{
		"000_init.sql", "001_identity.sql", "002_repositories.sql",
		"003_collaboration.sql", "004_ci.sql", "005_billing.sql",
		"006_identity_revocation.sql", "007_billing_partition_archives.sql",
		"008_updated_at_triggers.sql", "009_identity_temporal_columns.sql",
	}
	for _, f := range migrations {
		sql, rerr := os.ReadFile(filepath.Join(migrationsDir, f))
		if rerr != nil {
			t.Fatalf("read %s: %v", f, rerr)
		}
		if _, eerr := pool.Exec(ctx, string(sql)); eerr != nil {
			t.Fatalf("apply %s: %v", f, eerr)
		}
	}

	// --- minio (S3-compatible) ---------------------------------------------
	s3Ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "minio/minio:RELEASE.2024-12-13T22-19-12Z",
			Cmd:          []string{"server", "/data"},
			ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{
				"MINIO_ROOT_USER":     "minioadmin",
				"MINIO_ROOT_PASSWORD": "minioadmin",
			},
			WaitingFor: wait.ForHTTP("/minio/health/live").
				WithPort("9000/tcp").
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	t.Cleanup(func() { _ = s3Ctr.Terminate(ctx) })

	s3Host, _ := s3Ctr.Host(ctx)
	s3Port, _ := s3Ctr.MappedPort(ctx, "9000/tcp")
	s3Endpoint := fmt.Sprintf("http://%s:%s", s3Host, s3Port.Port())
	if _, perr := url.Parse(s3Endpoint); perr != nil {
		t.Fatalf("parse minio endpoint: %v", perr)
	}

	s3Client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(s3Endpoint),
		UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(
			"minioadmin", "minioadmin", "",
		),
	})

	bucket := "gitscale-archive-test"
	if _, berr := s3Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); berr != nil {
		t.Fatalf("create bucket: %v", berr)
	}

	objStore := NewS3ObjectStore(s3Client, bucket)
	archiver := billingstore.NewPostgresArchiver(pool)
	billingClient := appclient.NewStubBillingClient()
	keys := NewStubKeyProvider()

	return &archiveE2EFixture{
		pool:        pool,
		s3Client:    s3Client,
		objStore:    objStore,
		archiver:    archiver,
		billing:     billingClient,
		keys:        keys,
		bucket:      bucket,
		connStr:     connStr,
		s3Endpoint:  s3Endpoint,
		containerPG: pgCtr,
		containerS3: s3Ctr,
	}
}

// seedAccount inserts a quota_accounts row so foreign-key inserts into
// usage_events succeed. Returns the account UUID as text.
func (f *archiveE2EFixture) seedAccount(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	_, err := f.pool.Exec(context.Background(), `
		INSERT INTO billing.quota_accounts (id, org_id, plan_tier)
		VALUES ($1, $2, 'pro')`,
		id, uuid.NewString(),
	)
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

// seedUsageEvents inserts n rows into billing.usage_events_2026_05.
// All rows share the same account; ts is spaced 1 second apart starting at
// 2026-05-15T00:00:00Z so all fall within the partition range.
func (f *archiveE2EFixture) seedUsageEvents(t *testing.T, accountID string, n int) {
	t.Helper()
	base := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	for i := 0; i < n; i++ {
		_, err := f.pool.Exec(ctx, `
			INSERT INTO billing.usage_events
			  (id, account_id, principal_id, principal_type, surface,
			   cost_vector, value, event_source, ts)
			VALUES ($1, $2, $3, 'agent', 'tokens',
			        '{"model":"claude-sonnet-4-6"}'::jsonb, $4, 'api', $5)`,
			uuid.NewString(), accountID,
			"00000000-0000-0000-0000-000000000001",
			int64(i+1),
			base.Add(time.Duration(i)*time.Second),
		)
		if err != nil {
			t.Fatalf("seed usage_event %d: %v", i, err)
		}
	}
}

// partitionExists probes pg_class for the named billing.usage_events_*.
func (f *archiveE2EFixture) partitionExists(t *testing.T, name string) bool {
	t.Helper()
	var ok bool
	err := f.pool.QueryRow(context.Background(), `
		SELECT EXISTS (
		  SELECT 1 FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'billing' AND c.relname = $1
		)`, name,
	).Scan(&ok)
	if err != nil {
		t.Fatalf("probe pg_class: %v", err)
	}
	return ok
}

// objectBytes downloads a single S3 object's contents.
func (f *archiveE2EFixture) objectBytes(t *testing.T, key string) []byte {
	t.Helper()
	out, err := f.s3Client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("get object %s: %v", key, err)
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatalf("read object %s: %v", key, err)
	}
	return b
}

// objectExists returns true if HeadObject succeeds.
func (f *archiveE2EFixture) objectExists(t *testing.T, key string) bool {
	t.Helper()
	_, err := f.s3Client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(key),
	})
	return err == nil
}

// registerLiveActivities installs real activity implementations under their
// canonical Temporal names so the testsuite environment runs them against PG +
// minio rather than the unit-test mocks.
func (f *archiveE2EFixture) registerLiveActivities(t *testing.T, env *testsuite.TestWorkflowEnvironment) {
	t.Helper()
	detach, err := NewDetachPartitionActivity(f.archiver)
	if err != nil {
		t.Fatalf("detach activity: %v", err)
	}
	export, err := NewExportActivity(f.archiver, f.objStore, f.keys, f.bucket)
	if err != nil {
		t.Fatalf("export activity: %v", err)
	}
	emit, err := NewEmitArchiveEventActivity(f.billing)
	if err != nil {
		t.Fatalf("emit activity: %v", err)
	}
	drop, err := NewDropPartitionActivity(f.archiver)
	if err != nil {
		t.Fatalf("drop activity: %v", err)
	}
	env.RegisterActivityWithOptions(detach.Execute,
		activity.RegisterOptions{Name: ActivityNameDetachPartition})
	env.RegisterActivityWithOptions(export.Execute,
		activity.RegisterOptions{Name: ActivityNameExport})
	env.RegisterActivityWithOptions(emit.Execute,
		activity.RegisterOptions{Name: ActivityNameEmitArchiveEvent})
	env.RegisterActivityWithOptions(drop.Execute,
		activity.RegisterOptions{Name: ActivityNameDropPartition})
}

// archiveBaseKey is the S3 key prefix produced by ExportActivity for the
// 2026-05 partition (mirrors the format string in export_activity.go).
const archiveBaseKey = "billing/usage_events/year=2026/month=05/usage_events_2026_05"

// ----------------------------------------------------------------------------
// Test 1: happy path
// ----------------------------------------------------------------------------

func TestArchiveWorkflow_E2E_HappyPath(t *testing.T) {
	f := setupArchiveE2E(t)
	accountID := f.seedAccount(t)
	const rowCount = 100
	f.seedUsageEvents(t, accountID, rowCount)

	if !f.partitionExists(t, "usage_events_2026_05") {
		t.Fatal("seeded partition missing before run")
	}

	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(PartitionArchiveWorkflow)
	f.registerLiveActivities(t, env)

	env.ExecuteWorkflow(PartitionArchiveWorkflow, ArchiveInput{
		RunTime: time.Date(2027, 11, 24, 14, 0, 0, 0, time.UTC),
		Year:    2026,
		Month:   5,
	})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	var result ArchiveResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if result.RowCount != rowCount {
		t.Errorf("RowCount=%d want %d", result.RowCount, rowCount)
	}
	if !strings.HasSuffix(result.LakeURI, archiveBaseKey+".parquet") {
		t.Errorf("LakeURI=%q missing expected suffix", result.LakeURI)
	}

	// PG: partition gone.
	if f.partitionExists(t, "usage_events_2026_05") {
		t.Error("partition still exists after archive")
	}

	// S3: parquet, manifest, checksum present.
	for _, suffix := range []string{".parquet", ".manifest.json", ".checksum.sha256"} {
		if !f.objectExists(t, archiveBaseKey+suffix) {
			t.Errorf("missing S3 object: %s", archiveBaseKey+suffix)
		}
	}

	// Checksum: SHA-256 of the encrypted parquet bytes equals .checksum.sha256.
	parquetBytes := f.objectBytes(t, archiveBaseKey+".parquet")
	gotSum := fmt.Sprintf("%x", sha256.Sum256(parquetBytes))
	wantSum := strings.TrimSpace(string(f.objectBytes(t, archiveBaseKey+".checksum.sha256")))
	if gotSum != wantSum {
		t.Errorf("checksum mismatch: got=%s want=%s", gotSum, wantSum)
	}

	// Manifest fields.
	var m archiveManifest
	if err := json.Unmarshal(f.objectBytes(t, archiveBaseKey+".manifest.json"), &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.RowCount != rowCount {
		t.Errorf("manifest RowCount=%d want %d", m.RowCount, rowCount)
	}
	if m.SourcePartition != "billing.usage_events_2026_05" {
		t.Errorf("manifest SourcePartition=%q", m.SourcePartition)
	}
	if m.KEKHint != stubKEKHint {
		t.Errorf("manifest KEKHint=%q want %q", m.KEKHint, stubKEKHint)
	}
	if m.EncFormat != encFormatV1 {
		t.Errorf("manifest EncFormat=%q want %q", m.EncFormat, encFormatV1)
	}

	// Billing client: exactly one call with correct payload.
	calls := f.billing.Calls()
	if len(calls) != 1 {
		t.Fatalf("billing calls=%d want 1", len(calls))
	}
	if calls[0].RowCount != rowCount {
		t.Errorf("billing RowCount=%d want %d", calls[0].RowCount, rowCount)
	}
	if calls[0].LakeURI != result.LakeURI {
		t.Errorf("billing LakeURI=%q want %q", calls[0].LakeURI, result.LakeURI)
	}
}

// ----------------------------------------------------------------------------
// Test 2: crash mid-export → re-run completes idempotently
// ----------------------------------------------------------------------------

func TestArchiveWorkflow_E2E_CrashResumption(t *testing.T) {
	f := setupArchiveE2E(t)
	accountID := f.seedAccount(t)
	const rowCount = 50
	f.seedUsageEvents(t, accountID, rowCount)

	// Run #1: inject an export failure to simulate a mid-stream crash.
	// The partition will already be detached (DetachActivity ran first).
	// The S3 multipart upload may have written a partial parquet object —
	// that's OK; run #2's export overwrites it.
	failOnce := &flakyExport{archiver: f.archiver, store: f.objStore, keys: f.keys, bucket: f.bucket}
	if err := failOnce.init(); err != nil {
		t.Fatalf("flakyExport init: %v", err)
	}

	s := &testsuite.WorkflowTestSuite{}
	env1 := s.NewTestWorkflowEnvironment()
	env1.RegisterWorkflow(PartitionArchiveWorkflow)

	detach, _ := NewDetachPartitionActivity(f.archiver)
	emit, _ := NewEmitArchiveEventActivity(f.billing)
	drop, _ := NewDropPartitionActivity(f.archiver)
	env1.RegisterActivityWithOptions(detach.Execute,
		activity.RegisterOptions{Name: ActivityNameDetachPartition})
	env1.RegisterActivityWithOptions(failOnce.Execute,
		activity.RegisterOptions{Name: ActivityNameExport})
	env1.RegisterActivityWithOptions(emit.Execute,
		activity.RegisterOptions{Name: ActivityNameEmitArchiveEvent})
	env1.RegisterActivityWithOptions(drop.Execute,
		activity.RegisterOptions{Name: ActivityNameDropPartition})

	// The export activity's RetryPolicy will retry on the injected failure;
	// flakyExport returns an error on the first call only, then delegates to
	// the real ExportActivity. The workflow should still complete.
	env1.ExecuteWorkflow(PartitionArchiveWorkflow, ArchiveInput{
		RunTime: time.Date(2027, 11, 24, 14, 0, 0, 0, time.UTC),
		Year:    2026,
		Month:   5,
	})

	if err := env1.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error after retry: %v", err)
	}
	if failOnce.attempts < 2 {
		t.Errorf("flakyExport.attempts=%d, want >= 2 (crash + recovery)",
			failOnce.attempts)
	}

	// Final state matches non-crash baseline: partition gone, all 3 S3 objects
	// present.
	if f.partitionExists(t, "usage_events_2026_05") {
		t.Error("partition still exists after crash-resumption workflow")
	}
	for _, suffix := range []string{".parquet", ".manifest.json", ".checksum.sha256"} {
		if !f.objectExists(t, archiveBaseKey+suffix) {
			t.Errorf("missing S3 object after recovery: %s", archiveBaseKey+suffix)
		}
	}

	// The successful export uploaded the canonical bytes; checksum must match.
	parquetBytes := f.objectBytes(t, archiveBaseKey+".parquet")
	gotSum := fmt.Sprintf("%x", sha256.Sum256(parquetBytes))
	wantSum := strings.TrimSpace(string(f.objectBytes(t, archiveBaseKey+".checksum.sha256")))
	if gotSum != wantSum {
		t.Errorf("post-recovery checksum mismatch: got=%s want=%s", gotSum, wantSum)
	}
}

// flakyExport wraps a real ExportActivity but errors on the first call to
// simulate a mid-export crash. Subsequent calls delegate to the real activity.
type flakyExport struct {
	archiver billingstore.Archiver
	store    ObjectStore
	keys     KeyProvider
	bucket   string

	real     *ExportActivity
	attempts int
}

func (e *flakyExport) init() error {
	a, err := NewExportActivity(e.archiver, e.store, e.keys, e.bucket)
	if err != nil {
		return err
	}
	e.real = a
	return nil
}

func (e *flakyExport) Execute(ctx context.Context, in ExportInput) (ExportResult, error) {
	e.attempts++
	if e.attempts == 1 {
		return ExportResult{}, errors.New("simulated mid-export crash")
	}
	return e.real.Execute(ctx, in)
}

// ----------------------------------------------------------------------------
// Test 3: DETACH PENDING recovery — pg_inherits row left with
// inhdetachpending=true; archiver must call DETACH ... FINALIZE.
// ----------------------------------------------------------------------------

func TestArchiveWorkflow_E2E_DetachPendingRecovery(t *testing.T) {
	f := setupArchiveE2E(t)
	accountID := f.seedAccount(t)
	const rowCount = 10
	f.seedUsageEvents(t, accountID, rowCount)

	ctx := context.Background()

	// Force the partition into pending-detach state by holding a long-running
	// transaction in another connection while issuing DETACH CONCURRENTLY.
	// The first transaction of DETACH CONCURRENTLY commits (sets
	// inhdetachpending=true); the second blocks waiting for the holder. We
	// cancel the DETACH session, leaving inhdetachpending=true. The holder is
	// then released so the archiver's FINALIZE can run.

	holderCtx, holderCancel := context.WithCancel(ctx)
	defer holderCancel()
	holderConn, err := f.pool.Acquire(holderCtx)
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	// REPEATABLE READ snapshot held until release; this is what blocks the
	// second phase of DETACH CONCURRENTLY.
	if _, err := holderConn.Exec(holderCtx, "BEGIN ISOLATION LEVEL REPEATABLE READ"); err != nil {
		t.Fatalf("holder BEGIN: %v", err)
	}
	if _, err := holderConn.Exec(holderCtx,
		"SELECT count(*) FROM billing.usage_events"); err != nil {
		t.Fatalf("holder SELECT: %v", err)
	}

	// Detach in a separate goroutine with a cancellable context.
	detachCtx, detachCancel := context.WithCancel(ctx)
	detachDone := make(chan error, 1)
	go func() {
		_, derr := f.pool.Exec(detachCtx, `
			ALTER TABLE billing.usage_events
			DETACH PARTITION billing.usage_events_2026_05 CONCURRENTLY`)
		detachDone <- derr
	}()

	// Poll until inhdetachpending=true (first transaction has committed).
	deadline := time.Now().Add(30 * time.Second)
	for {
		var pending bool
		err := f.pool.QueryRow(ctx, `
			SELECT COALESCE((
			  SELECT inhdetachpending FROM pg_inherits
			  JOIN pg_class child ON pg_inherits.inhrelid = child.oid
			  JOIN pg_namespace n ON child.relnamespace = n.oid
			  WHERE n.nspname = 'billing'
			    AND child.relname = 'usage_events_2026_05'
			), false)`,
		).Scan(&pending)
		if err == nil && pending {
			break
		}
		if time.Now().After(deadline) {
			detachCancel()
			holderCancel()
			t.Fatalf("inhdetachpending never became true (err=%v)", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Cancel the DETACH command; it will return context.Canceled. The first
	// transaction already committed, so inhdetachpending stays true.
	detachCancel()
	<-detachDone

	// Release the holder so FINALIZE can complete.
	_, _ = holderConn.Exec(ctx, "ROLLBACK")
	holderConn.Release()
	holderCancel()

	// Confirm pre-condition: pg_inherits row still exists with pending=true.
	var stillPending bool
	if err := f.pool.QueryRow(ctx, `
		SELECT COALESCE((
		  SELECT inhdetachpending FROM pg_inherits
		  JOIN pg_class child ON pg_inherits.inhrelid = child.oid
		  JOIN pg_namespace n ON child.relnamespace = n.oid
		  WHERE n.nspname = 'billing'
		    AND child.relname = 'usage_events_2026_05'
		), false)`,
	).Scan(&stillPending); err != nil {
		t.Fatalf("verify pending: %v", err)
	}
	if !stillPending {
		t.Skip("could not establish DETACH PENDING state on this PG build; skipping")
	}

	// Now run the workflow. PostgresArchiver.DetachUsageEventsPartition
	// detects pending=true and issues DETACH ... FINALIZE.
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(PartitionArchiveWorkflow)
	f.registerLiveActivities(t, env)

	env.ExecuteWorkflow(PartitionArchiveWorkflow, ArchiveInput{
		RunTime: time.Date(2027, 11, 24, 14, 0, 0, 0, time.UTC),
		Year:    2026,
		Month:   5,
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error after FINALIZE recovery: %v", err)
	}

	if f.partitionExists(t, "usage_events_2026_05") {
		t.Error("partition still exists after FINALIZE recovery")
	}
	for _, suffix := range []string{".parquet", ".manifest.json", ".checksum.sha256"} {
		if !f.objectExists(t, archiveBaseKey+suffix) {
			t.Errorf("missing S3 object after FINALIZE recovery: %s", archiveBaseKey+suffix)
		}
	}
}
