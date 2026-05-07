# Billing Partition Archive Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the `PartitionArchiveWorkflow` Temporal workflow that detaches billing `usage_events` partitions older than 18 months, streams them to an object store as encrypted Parquet+zstd, emits a `billing.partition_archived` outbox event, and drops the detached partition.

**Architecture:** Four activities in sequence — `DetachPartition → ExportToObjectStore → EmitOutbox → DropPartition` — orchestrated by a single Temporal workflow on `QueueBillingMaintenance`. All external dependencies (`Archiver`, `ObjectStore`, `KeyProvider`, `BillingClient`) are injected interfaces; the S3-compatible `ObjectStore` impl covers AWS S3, minio, Cloudflare R2, and GCS S3-interop without code changes. Outbox emit routes via `appclient.BillingClient` stub per ADR-019; the gRPC impl is a follow-up issue.

**Tech Stack:** Go, Temporal SDK, `github.com/parquet-go/parquet-go`, `github.com/aws/aws-sdk-go-v2/service/s3`, `pgx/v5`, AES-256-GCM (chunked streaming), minio (local dev + integration tests via testcontainers).

**Design spec:** `docs/superpowers/specs/2026-05-07-issue-69-archive-workflow-design.md`

---

## File Map

| Action | Path | Purpose |
|---|---|---|
| Create | `plane/data/store/billing/archiver.go` | `Archiver` interface, `RowCursor`, `UsageEventRow` |
| Create | `plane/data/store/billing/archiver_stub.go` | `StubArchiver` for tests |
| Create | `plane/data/store/billing/archiver_postgres.go` | `PostgresArchiver` production impl |
| Create | `plane/workflow/appclient/billing.go` | `BillingClient` interface + `StubBillingClient` |
| Create | `plane/workflow/billing/objectstore.go` | `ObjectStore` interface + `stubObjectStore` |
| Create | `plane/workflow/billing/keyprovider.go` | `KeyProvider` interface + `stubKeyProvider` |
| Create | `plane/workflow/billing/objectstore_s3.go` | `S3ObjectStore` production impl |
| Create | `plane/workflow/billing/detach_activity.go` | `DetachPartitionActivity` |
| Create | `plane/workflow/billing/drop_activity.go` | `DropPartitionActivity` |
| Create | `plane/workflow/billing/emit_activity.go` | `EmitArchiveEventActivity` |
| Create | `plane/workflow/billing/export_activity.go` | `ExportActivity` (parquet+encrypt+upload) |
| Create | `plane/workflow/billing/archive_workflow.go` | `PartitionArchiveWorkflow` |
| Create | `plane/workflow/billing/archive_schedules.go` | `EnsureArchiveSchedule` |
| Create | `plane/workflow/billing/archive_workflow_test.go` | Temporal test suite |
| Modify | `plane/workflow/billing/bundle.go` | Register archive workflow + activities |
| Modify | `docker-compose.yml` | Add minio service |

---

## Task 1: Add go.mod dependencies

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add parquet-go and AWS SDK v2 dependencies**

```bash
cd /path/to/worktree  # use your worktree path
go get github.com/parquet-go/parquet-go@latest
go get github.com/aws/aws-sdk-go-v2/aws@latest
go get github.com/aws/aws-sdk-go-v2/service/s3@latest
go get github.com/aws/aws-sdk-go-v2/feature/s3/manager@latest
go get github.com/aws/aws-sdk-go-v2/config@latest
go get github.com/aws/aws-sdk-go-v2/credentials@latest
```

- [ ] **Step 2: Verify build still passes**

```bash
go build ./...
```

Expected: exit 0, no errors.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore(deps): add parquet-go and aws-sdk-go-v2"
```

---

## Task 2: `billing.Archiver` interface, types, and stub

**Files:**
- Create: `plane/data/store/billing/archiver.go`
- Create: `plane/data/store/billing/archiver_stub.go`

- [ ] **Step 1: Create `archiver.go` with interface and types**

```go
// plane/data/store/billing/archiver.go
package billing

import (
	"context"
	"time"
)

// Archiver is the data-layer interface for the archive workflow activities.
// All methods must be idempotent — the workflow retry policy will replay them.
type Archiver interface {
	// DetachUsageEventsPartition issues ALTER TABLE … DETACH PARTITION … CONCURRENTLY.
	// No-op if the partition is already detached or does not exist as a child.
	DetachUsageEventsPartition(ctx context.Context, year, month int) error

	// DropUsageEventsPartition drops the detached billing.usage_events_YYYY_MM table.
	// Must only be called after the object store upload and outbox emit succeed.
	// Uses DROP TABLE IF EXISTS — idempotent.
	DropUsageEventsPartition(ctx context.Context, year, month int) error

	// ScanPartitionRows returns a cursor over all rows in the named partition.
	// The caller must call Close on the cursor regardless of error.
	ScanPartitionRows(ctx context.Context, year, month int) (RowCursor, error)
}

// RowCursor iterates over rows in a partition. Close must always be called.
type RowCursor interface {
	Next(ctx context.Context) bool
	Row() UsageEventRow
	Err() error
	Close() error
}

// UsageEventRow mirrors the billing.usage_events column set from 005_billing.sql.
// String types are used for UUIDs and the surface enum so the Parquet file is
// readable by Athena/Trino/DuckDB without binary decoding.
// Pointer fields are nullable columns in Parquet (optional in schema).
type UsageEventRow struct {
	ID              string    `parquet:"id"`
	AccountID       string    `parquet:"account_id"`
	PrincipalID     string    `parquet:"principal_id"`
	PrincipalType   string    `parquet:"principal_type"`
	Surface         string    `parquet:"surface"`
	CostVector      string    `parquet:"cost_vector"`
	Value           int64     `parquet:"value"`
	RepoID          *string   `parquet:"repo_id"`
	EventSource     string    `parquet:"event_source"`
	ExternalEventID *string   `parquet:"external_event_id"`
	Ts              time.Time `parquet:"ts"`
	CreatedAt       time.Time `parquet:"created_at"`
}
```

- [ ] **Step 2: Create `archiver_stub.go`**

```go
// plane/data/store/billing/archiver_stub.go
package billing

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// StubArchiver is an in-memory Archiver for workflow unit tests.
type StubArchiver struct {
	mu       sync.Mutex
	detached map[string]bool
	dropped  map[string]bool
	rows     map[string][]UsageEventRow

	DetachFn func(year, month int) error
	DropFn   func(year, month int) error
}

// NewStubArchiver returns a StubArchiver with an empty row set. Use SetRows
// to inject test data before running an activity.
func NewStubArchiver() *StubArchiver {
	return &StubArchiver{
		detached: map[string]bool{},
		dropped:  map[string]bool{},
		rows:     map[string][]UsageEventRow{},
	}
}

// SetRows seeds the stub with rows for the given (year, month) partition.
func (s *StubArchiver) SetRows(year, month int, rows []UsageEventRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[partitionKey(year, month)] = rows
}

func (s *StubArchiver) DetachUsageEventsPartition(_ context.Context, year, month int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DetachFn != nil {
		if err := s.DetachFn(year, month); err != nil {
			return err
		}
	}
	s.detached[partitionKey(year, month)] = true
	return nil
}

func (s *StubArchiver) DropUsageEventsPartition(_ context.Context, year, month int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DropFn != nil {
		if err := s.DropFn(year, month); err != nil {
			return err
		}
	}
	s.dropped[partitionKey(year, month)] = true
	return nil
}

func (s *StubArchiver) ScanPartitionRows(_ context.Context, year, month int) (RowCursor, error) {
	s.mu.Lock()
	rows := append([]UsageEventRow(nil), s.rows[partitionKey(year, month)]...)
	s.mu.Unlock()
	return &stubCursor{rows: rows}, nil
}

// IsDetached reports whether DetachUsageEventsPartition was called for (year, month).
func (s *StubArchiver) IsDetached(year, month int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached[partitionKey(year, month)]
}

// IsDropped reports whether DropUsageEventsPartition was called for (year, month).
func (s *StubArchiver) IsDropped(year, month int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped[partitionKey(year, month)]
}

type stubCursor struct {
	rows []UsageEventRow
	pos  int
	cur  UsageEventRow
}

func (c *stubCursor) Next(_ context.Context) bool {
	if c.pos >= len(c.rows) {
		return false
	}
	c.cur = c.rows[c.pos]
	c.pos++
	return true
}

func (c *stubCursor) Row() UsageEventRow { return c.cur }
func (c *stubCursor) Err() error         { return nil }
func (c *stubCursor) Close() error       { return nil }

func partitionKey(year, month int) string {
	return fmt.Sprintf("%04d_%02d", year, month)
}

// SeedUsageEventRow returns a representative UsageEventRow for a given timestamp.
// Used by tests to build deterministic fixtures.
func SeedUsageEventRow(id, accountID string, ts time.Time) UsageEventRow {
	return UsageEventRow{
		ID:            id,
		AccountID:     accountID,
		PrincipalID:   "00000000-0000-0000-0000-000000000001",
		PrincipalType: "agent",
		Surface:       "tokens",
		CostVector:    `{"model":"claude-sonnet-4-6"}`,
		Value:         1000,
		EventSource:   "api",
		Ts:            ts,
		CreatedAt:     ts,
	}
}
```

- [ ] **Step 3: Verify compilation**

```bash
go build ./plane/data/store/billing/...
```

Expected: exit 0.

- [ ] **Step 4: Commit**

```bash
git add plane/data/store/billing/archiver.go plane/data/store/billing/archiver_stub.go
git commit -m "feat(data/billing): Archiver interface, UsageEventRow, StubArchiver"
```

---

## Task 3: `PostgresArchiver` production implementation

**Files:**
- Create: `plane/data/store/billing/archiver_postgres.go`
- Create: `plane/data/store/billing/archiver_postgres_test.go`

- [ ] **Step 1: Write the failing test**

```go
// plane/data/store/billing/archiver_postgres_test.go
package billing

import (
	"testing"
)

// TestStubArchiver_DetachAndDrop validates the stub used by workflow unit tests.
// The postgres impl is covered by the integration test (-tags integration).
func TestStubArchiver_DetachAndDrop(t *testing.T) {
	a := NewStubArchiver()

	ctx := t.Context()

	if err := a.DetachUsageEventsPartition(ctx, 2026, 5); err != nil {
		t.Fatal(err)
	}
	if !a.IsDetached(2026, 5) {
		t.Error("expected partition to be detached")
	}

	if err := a.DropUsageEventsPartition(ctx, 2026, 5); err != nil {
		t.Fatal(err)
	}
	if !a.IsDropped(2026, 5) {
		t.Error("expected partition to be dropped")
	}
}

func TestStubArchiver_ScanRows(t *testing.T) {
	a := NewStubArchiver()
	ts := mustParseTime("2026-05-15T10:00:00Z")
	a.SetRows(2026, 5, []UsageEventRow{
		SeedUsageEventRow("id-1", "acc-1", ts),
		SeedUsageEventRow("id-2", "acc-1", ts),
	})

	ctx := t.Context()
	cur, err := a.ScanPartitionRows(ctx, 2026, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close()

	var count int
	for cur.Next(ctx) {
		count++
		_ = cur.Row()
	}
	if cur.Err() != nil {
		t.Fatal(cur.Err())
	}
	if count != 2 {
		t.Errorf("got %d rows, want 2", count)
	}
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
```

- [ ] **Step 2: Run test to confirm it fails for the right reason**

```bash
go test ./plane/data/store/billing/... -run TestStubArchiver -v
```

Expected: compile error (missing `time` import in test file or missing `t.Context()`).

Fix any import errors, then confirm both tests PASS (stub tests should pass, they test the stub not postgres).

- [ ] **Step 3: Create `archiver_postgres.go`**

```go
// plane/data/store/billing/archiver_postgres.go
package billing

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresArchiver is the production Archiver backed by pgxpool.
type PostgresArchiver struct {
	pool *pgxpool.Pool
}

// NewPostgresArchiver returns a PostgresArchiver. The pool is not owned by
// this struct; the caller is responsible for closing it.
func NewPostgresArchiver(pool *pgxpool.Pool) *PostgresArchiver {
	return &PostgresArchiver{pool: pool}
}

// DetachUsageEventsPartition detaches the named partition from billing.usage_events.
// Idempotent: checks pg_inherits first; no-op if already detached.
func (a *PostgresArchiver) DetachUsageEventsPartition(ctx context.Context, year, month int) error {
	tableName := fmt.Sprintf("usage_events_%04d_%02d", year, month)

	var attached bool
	err := a.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_inherits
			JOIN pg_class parent ON pg_inherits.inhparent = parent.oid
			JOIN pg_class child  ON pg_inherits.inhrelid  = child.oid
			JOIN pg_namespace ns ON parent.relnamespace   = ns.oid
			WHERE ns.nspname    = 'billing'
			  AND parent.relname = 'usage_events'
			  AND child.relname  = $1
		)`, tableName,
	).Scan(&attached)
	if err != nil {
		return fmt.Errorf("archiver: probe pg_inherits: %w", err)
	}
	if !attached {
		return nil // already detached
	}

	_, err = a.pool.Exec(ctx, fmt.Sprintf(
		"ALTER TABLE billing.usage_events DETACH PARTITION billing.%s CONCURRENTLY",
		tableName,
	))
	if err != nil {
		return fmt.Errorf("archiver: detach %s: %w", tableName, err)
	}
	return nil
}

// DropUsageEventsPartition drops the detached partition table.
// Uses DROP TABLE IF EXISTS — idempotent.
func (a *PostgresArchiver) DropUsageEventsPartition(ctx context.Context, year, month int) error {
	tableName := fmt.Sprintf("usage_events_%04d_%02d", year, month)
	_, err := a.pool.Exec(ctx,
		fmt.Sprintf("DROP TABLE IF EXISTS billing.%s", tableName),
	)
	if err != nil {
		return fmt.Errorf("archiver: drop %s: %w", tableName, err)
	}
	return nil
}

// ScanPartitionRows opens a server-side cursor over all rows in the detached
// partition, ordered by (ts, id). The caller must call Close.
func (a *PostgresArchiver) ScanPartitionRows(ctx context.Context, year, month int) (RowCursor, error) {
	tableName := fmt.Sprintf("usage_events_%04d_%02d", year, month)
	query := fmt.Sprintf(`
		SELECT id::text, account_id::text, principal_id::text, principal_type,
		       surface::text, cost_vector::text, value, repo_id::text,
		       event_source, external_event_id::text, ts, created_at
		FROM billing.%s
		ORDER BY ts, id`, tableName)

	rows, err := a.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("archiver: scan %s: %w", tableName, err)
	}
	return &pgxCursor{rows: rows}, nil
}

type pgxCursor struct {
	rows pgx.Rows
	cur  UsageEventRow
	err  error
}

func (c *pgxCursor) Next(_ context.Context) bool {
	if !c.rows.Next() {
		return false
	}
	var repoID, extID *string
	c.err = c.rows.Scan(
		&c.cur.ID, &c.cur.AccountID, &c.cur.PrincipalID, &c.cur.PrincipalType,
		&c.cur.Surface, &c.cur.CostVector, &c.cur.Value, &repoID,
		&c.cur.EventSource, &extID, &c.cur.Ts, &c.cur.CreatedAt,
	)
	c.cur.RepoID = repoID
	c.cur.ExternalEventID = extID
	return c.err == nil
}

func (c *pgxCursor) Row() UsageEventRow { return c.cur }
func (c *pgxCursor) Err() error {
	if c.err != nil {
		return c.err
	}
	return c.rows.Err()
}
func (c *pgxCursor) Close() error {
	c.rows.Close()
	return nil
}
```

- [ ] **Step 4: Build**

```bash
go build ./plane/data/store/billing/...
```

Expected: exit 0.

- [ ] **Step 5: Run stub tests**

```bash
go test ./plane/data/store/billing/... -v
```

Expected: `TestStubArchiver_DetachAndDrop` PASS, `TestStubArchiver_ScanRows` PASS.

- [ ] **Step 6: Commit**

```bash
git add plane/data/store/billing/archiver_postgres.go plane/data/store/billing/archiver_postgres_test.go
git commit -m "feat(data/billing): PostgresArchiver — detach, drop, scan partition rows"
```

---

## Task 4: `appclient.BillingClient` interface + stub

**Files:**
- Create: `plane/workflow/appclient/billing.go`

- [ ] **Step 1: Create `billing.go`**

```go
// plane/workflow/appclient/billing.go
package appclient

import (
	"context"
	"sync"
)

// BillingClient is the workflow-plane view of the application-plane billing
// service. RecordPartitionArchived corresponds to a gRPC unary call that writes
// billing.partition_archived to the billing_outbox in a single transaction
// (ADR-008 + ADR-019). The gRPC implementation is a follow-up issue.
type BillingClient interface {
	// RecordPartitionArchived emits billing.partition_archived to the outbox
	// via the billing app-plane service.
	RecordPartitionArchived(ctx context.Context, in PartitionArchivedInput) error
}

// PartitionArchivedInput carries the archival outcome to the billing service.
type PartitionArchivedInput struct {
	Year          int
	Month         int
	PartitionName string
	LakeURI       string // canonical URI returned by ObjectStore.Upload
	RowCount      int64
	BytesWritten  int64
}

// StubBillingClient records calls in memory. Used by workflow unit tests.
type StubBillingClient struct {
	mu    sync.Mutex
	calls []PartitionArchivedInput
	fn    func(in PartitionArchivedInput) error
}

// NewStubBillingClient returns a recording stub that succeeds by default.
func NewStubBillingClient() *StubBillingClient { return &StubBillingClient{} }

// SetFn injects a fake-error path.
func (s *StubBillingClient) SetFn(fn func(PartitionArchivedInput) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn = fn
}

// RecordPartitionArchived records the call and runs the injected fn if set.
func (s *StubBillingClient) RecordPartitionArchived(_ context.Context, in PartitionArchivedInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fn != nil {
		if err := s.fn(in); err != nil {
			return err
		}
	}
	s.calls = append(s.calls, in)
	return nil
}

// Calls returns a snapshot of all recorded calls.
func (s *StubBillingClient) Calls() []PartitionArchivedInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PartitionArchivedInput(nil), s.calls...)
}
```

- [ ] **Step 2: Build**

```bash
go build ./plane/workflow/appclient/...
```

Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
git add plane/workflow/appclient/billing.go
git commit -m "feat(workflow/appclient): BillingClient interface + StubBillingClient"
```

---

## Task 5: `ObjectStore` and `KeyProvider` interfaces + stubs

**Files:**
- Create: `plane/workflow/billing/objectstore.go`
- Create: `plane/workflow/billing/keyprovider.go`

- [ ] **Step 1: Create `objectstore.go`**

```go
// plane/workflow/billing/objectstore.go
package billing

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// ObjectStore is a provider-agnostic interface for writing archive objects.
// Implementations cover AWS S3, minio, Cloudflare R2, and GCS S3-interop mode
// by pointing the S3-compatible client at the appropriate endpoint.
type ObjectStore interface {
	// Upload streams r to key. The implementation handles multipart upload
	// internally for large objects. sizeHint is -1 when unknown.
	// Returns the canonical URI (e.g. "s3://bucket/key") for the outbox event.
	Upload(ctx context.Context, key string, r io.Reader, sizeHint int64) (uri string, err error)

	// PutBytes writes a small object (manifest JSON, checksum file).
	PutBytes(ctx context.Context, key string, data []byte) error
}

// stubObjectStore captures uploaded objects in memory. Used by activity unit tests.
type stubObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	bucket  string
	uploadFn func(key string) error
}

func newStubObjectStore(bucket string) *stubObjectStore {
	return &stubObjectStore{bucket: bucket, objects: map[string][]byte{}}
}

// SetUploadFn injects an error path triggered on Upload.
func (s *stubObjectStore) SetUploadFn(fn func(key string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploadFn = fn
}

func (s *stubObjectStore) Upload(_ context.Context, key string, r io.Reader, _ int64) (string, error) {
	s.mu.Lock()
	fn := s.uploadFn
	s.mu.Unlock()
	if fn != nil {
		if err := fn(key); err != nil {
			return "", err
		}
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.objects[key] = data
	s.mu.Unlock()
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

func (s *stubObjectStore) PutBytes(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), data...)
	return nil
}

// Get returns the stored bytes for key, or nil if absent.
func (s *stubObjectStore) Get(key string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[key]
}
```

- [ ] **Step 2: Create `keyprovider.go`**

```go
// plane/workflow/billing/keyprovider.go
package billing

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
)

// KeyProvider derives encryption keys for archive files.
// Production wires HashiCorp Vault transit HKDF; the stub uses a deterministic
// derivation safe for tests and local dev.
type KeyProvider interface {
	// GetDEK returns a 32-byte AES-256 key for the given (year, month).
	GetDEK(ctx context.Context, year, month int) ([]byte, error)
}

// stubKeyProvider derives keys deterministically from (year, month).
// Never use in production — keys are predictable.
type stubKeyProvider struct{}

// NewStubKeyProvider returns a deterministic KeyProvider for tests.
func NewStubKeyProvider() KeyProvider { return stubKeyProvider{} }

func (stubKeyProvider) GetDEK(_ context.Context, year, month int) ([]byte, error) {
	// Deterministic: SHA-256(year || month). Predictable but sufficient for tests.
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[:4], uint32(year))
	binary.BigEndian.PutUint32(buf[4:], uint32(month))
	h.Write(buf[:])
	return h.Sum(nil), nil
}
```

- [ ] **Step 3: Build**

```bash
go build ./plane/workflow/billing/...
```

Expected: exit 0.

- [ ] **Step 4: Commit**

```bash
git add plane/workflow/billing/objectstore.go plane/workflow/billing/keyprovider.go
git commit -m "feat(workflow/billing): ObjectStore and KeyProvider interfaces + stubs"
```

---

## Task 6: `S3ObjectStore` production implementation

**Files:**
- Create: `plane/workflow/billing/objectstore_s3.go`

- [ ] **Step 1: Create `objectstore_s3.go`**

```go
// plane/workflow/billing/objectstore_s3.go
package billing

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3ObjectStore is the production ObjectStore backed by any S3-compatible
// endpoint. Configure the underlying *s3.Client with a custom EndpointResolverV2
// to point at minio (local dev), Cloudflare R2, or GCS S3-interop mode.
type S3ObjectStore struct {
	client   *s3.Client
	uploader *manager.Uploader
	bucket   string
}

// NewS3ObjectStore returns an S3ObjectStore for bucket backed by client.
// Part size defaults to 64 MiB — appropriate for 60 GB Parquet files.
func NewS3ObjectStore(client *s3.Client, bucket string) *S3ObjectStore {
	up := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = 64 * 1024 * 1024 // 64 MiB
	})
	return &S3ObjectStore{client: client, uploader: up, bucket: bucket}
}

// Upload streams r to key using S3 multipart upload. sizeHint is passed as
// ContentLength when non-negative (improves progress tracking; safe to omit).
func (s *S3ObjectStore) Upload(ctx context.Context, key string, r io.Reader, sizeHint int64) (string, error) {
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   r,
	}
	if sizeHint >= 0 {
		input.ContentLength = aws.Int64(sizeHint)
	}
	if _, err := s.uploader.Upload(ctx, input); err != nil {
		return "", fmt.Errorf("s3: upload %s: %w", key, err)
	}
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

// PutBytes writes small objects (manifest, checksum) using a simple PutObject.
func (s *S3ObjectStore) PutBytes(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return fmt.Errorf("s3: put %s: %w", key, err)
	}
	return nil
}
```

- [ ] **Step 2: Build**

```bash
go build ./plane/workflow/billing/...
```

Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
git add plane/workflow/billing/objectstore_s3.go
git commit -m "feat(workflow/billing): S3ObjectStore — S3-compatible multipart upload"
```

---

## Task 7: `DetachPartitionActivity` (TDD)

**Files:**
- Create: `plane/workflow/billing/detach_activity.go`
- Create: `plane/workflow/billing/detach_activity_test.go`

- [ ] **Step 1: Write the failing test**

```go
// plane/workflow/billing/detach_activity_test.go
package billing

import (
	"context"
	"errors"
	"testing"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

func TestDetachPartitionActivity_callsArchiver(t *testing.T) {
	stub := billingstore.NewStubArchiver()
	act, err := NewDetachPartitionActivity(stub)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	err = act.Execute(ctx, DetachInput{Year: 2026, Month: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !stub.IsDetached(2026, 5) {
		t.Error("expected partition to be detached")
	}
}

func TestDetachPartitionActivity_propagatesError(t *testing.T) {
	stub := billingstore.NewStubArchiver()
	stub.DetachFn = func(year, month int) error {
		return errors.New("pg: connection reset")
	}
	act, _ := NewDetachPartitionActivity(stub)

	err := act.Execute(context.Background(), DetachInput{Year: 2026, Month: 5})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestNewDetachPartitionActivity_nilArchiver(t *testing.T) {
	if _, err := NewDetachPartitionActivity(nil); err == nil {
		t.Error("expected error for nil archiver")
	}
}
```

- [ ] **Step 2: Run test — confirm compile error (activity not defined yet)**

```bash
go test ./plane/workflow/billing/... -run TestDetachPartition -v
```

Expected: compile error: `undefined: DetachInput` / `undefined: NewDetachPartitionActivity`.

- [ ] **Step 3: Create `detach_activity.go`**

```go
// plane/workflow/billing/detach_activity.go
package billing

import (
	"context"
	"errors"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

const ActivityNameDetachPartition = "billing.DetachPartition"

// DetachInput is the input to DetachPartitionActivity.Execute.
type DetachInput struct {
	Year  int
	Month int
}

// DetachPartitionActivity issues DETACH PARTITION CONCURRENTLY via the Archiver.
type DetachPartitionActivity struct {
	archiver billingstore.Archiver
}

// NewDetachPartitionActivity returns a DetachPartitionActivity. Returns an
// error if archiver is nil so the worker boot path fails fast.
func NewDetachPartitionActivity(archiver billingstore.Archiver) (*DetachPartitionActivity, error) {
	if archiver == nil {
		return nil, errors.New("billing.NewDetachPartitionActivity: archiver is nil")
	}
	return &DetachPartitionActivity{archiver: archiver}, nil
}

// Execute detaches billing.usage_events_YYYY_MM from the parent table.
// Idempotent — the Archiver checks pg_inherits and skips if already detached.
func (a *DetachPartitionActivity) Execute(ctx context.Context, in DetachInput) error {
	return a.archiver.DetachUsageEventsPartition(ctx, in.Year, in.Month)
}
```

- [ ] **Step 4: Run tests — all pass**

```bash
go test ./plane/workflow/billing/... -run TestDetachPartition -v
```

Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/workflow/billing/detach_activity.go plane/workflow/billing/detach_activity_test.go
git commit -m "feat(workflow/billing): DetachPartitionActivity"
```

---

## Task 8: `DropPartitionActivity` (TDD)

**Files:**
- Create: `plane/workflow/billing/drop_activity.go`
- Create: `plane/workflow/billing/drop_activity_test.go`

- [ ] **Step 1: Write the failing test**

```go
// plane/workflow/billing/drop_activity_test.go
package billing

import (
	"context"
	"errors"
	"testing"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

func TestDropPartitionActivity_callsArchiver(t *testing.T) {
	stub := billingstore.NewStubArchiver()
	act, err := NewDropPartitionActivity(stub)
	if err != nil {
		t.Fatal(err)
	}

	if err := act.Execute(context.Background(), DropInput{Year: 2026, Month: 5}); err != nil {
		t.Fatal(err)
	}
	if !stub.IsDropped(2026, 5) {
		t.Error("expected partition to be dropped")
	}
}

func TestDropPartitionActivity_propagatesError(t *testing.T) {
	stub := billingstore.NewStubArchiver()
	stub.DropFn = func(_, _ int) error { return errors.New("pg: permission denied") }
	act, _ := NewDropPartitionActivity(stub)

	if err := act.Execute(context.Background(), DropInput{Year: 2026, Month: 5}); err == nil {
		t.Error("expected error, got nil")
	}
}

func TestNewDropPartitionActivity_nilArchiver(t *testing.T) {
	if _, err := NewDropPartitionActivity(nil); err == nil {
		t.Error("expected error for nil archiver")
	}
}
```

- [ ] **Step 2: Run — confirm compile error**

```bash
go test ./plane/workflow/billing/... -run TestDropPartition -v
```

Expected: compile error: `undefined: DropInput`.

- [ ] **Step 3: Create `drop_activity.go`**

```go
// plane/workflow/billing/drop_activity.go
package billing

import (
	"context"
	"errors"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

const ActivityNameDropPartition = "billing.DropPartition"

// DropInput is the input to DropPartitionActivity.Execute.
type DropInput struct {
	Year  int
	Month int
}

// DropPartitionActivity drops the detached billing.usage_events_YYYY_MM table.
// Must only be called after the object store upload and outbox emit succeed.
type DropPartitionActivity struct {
	archiver billingstore.Archiver
}

// NewDropPartitionActivity returns a DropPartitionActivity backed by archiver.
func NewDropPartitionActivity(archiver billingstore.Archiver) (*DropPartitionActivity, error) {
	if archiver == nil {
		return nil, errors.New("billing.NewDropPartitionActivity: archiver is nil")
	}
	return &DropPartitionActivity{archiver: archiver}, nil
}

// Execute drops billing.usage_events_YYYY_MM. Uses DROP TABLE IF EXISTS — idempotent.
func (a *DropPartitionActivity) Execute(ctx context.Context, in DropInput) error {
	return a.archiver.DropUsageEventsPartition(ctx, in.Year, in.Month)
}
```

- [ ] **Step 4: Run tests — all pass**

```bash
go test ./plane/workflow/billing/... -run TestDropPartition -v
```

Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/workflow/billing/drop_activity.go plane/workflow/billing/drop_activity_test.go
git commit -m "feat(workflow/billing): DropPartitionActivity"
```

---

## Task 9: `EmitArchiveEventActivity` (TDD)

**Files:**
- Create: `plane/workflow/billing/emit_activity.go`
- Create: `plane/workflow/billing/emit_activity_test.go`

- [ ] **Step 1: Write the failing test**

```go
// plane/workflow/billing/emit_activity_test.go
package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

func TestEmitArchiveEventActivity_callsBillingClient(t *testing.T) {
	stub := appclient.NewStubBillingClient()
	act, err := NewEmitArchiveEventActivity(stub)
	if err != nil {
		t.Fatal(err)
	}

	in := EmitInput{
		Year:          2026,
		Month:         5,
		PartitionName: "billing.usage_events_2026_05",
		LakeURI:       "s3://gitscale-analytics-test/billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet",
		RowCount:      1000,
		BytesWritten:  5000,
	}
	if err := act.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}

	calls := stub.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(calls))
	}
	got := calls[0]
	if got.LakeURI != in.LakeURI {
		t.Errorf("LakeURI=%s want %s", got.LakeURI, in.LakeURI)
	}
	if got.RowCount != in.RowCount {
		t.Errorf("RowCount=%d want %d", got.RowCount, in.RowCount)
	}
}

func TestEmitArchiveEventActivity_propagatesError(t *testing.T) {
	stub := appclient.NewStubBillingClient()
	stub.SetFn(func(_ appclient.PartitionArchivedInput) error {
		return errors.New("grpc: unavailable")
	})
	act, _ := NewEmitArchiveEventActivity(stub)

	err := act.Execute(context.Background(), EmitInput{Year: 2026, Month: 5})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestNewEmitArchiveEventActivity_nilClient(t *testing.T) {
	if _, err := NewEmitArchiveEventActivity(nil); err == nil {
		t.Error("expected error for nil client")
	}
}
```

- [ ] **Step 2: Run — confirm compile error**

```bash
go test ./plane/workflow/billing/... -run TestEmitArchiveEvent -v
```

Expected: compile error: `undefined: EmitInput`.

- [ ] **Step 3: Create `emit_activity.go`**

```go
// plane/workflow/billing/emit_activity.go
package billing

import (
	"context"
	"errors"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

const ActivityNameEmitArchiveEvent = "billing.EmitArchiveEvent"

// EmitInput is the input to EmitArchiveEventActivity.Execute.
type EmitInput struct {
	Year          int
	Month         int
	PartitionName string
	LakeURI       string
	RowCount      int64
	BytesWritten  int64
}

// EmitArchiveEventActivity calls appclient.BillingClient.RecordPartitionArchived,
// which routes to the billing app-plane service (ADR-019). The service writes
// billing.partition_archived to billing_outbox in a single transaction (ADR-008).
type EmitArchiveEventActivity struct {
	client appclient.BillingClient
}

// NewEmitArchiveEventActivity returns an EmitArchiveEventActivity. Returns an
// error if client is nil so the worker boot path fails fast.
func NewEmitArchiveEventActivity(client appclient.BillingClient) (*EmitArchiveEventActivity, error) {
	if client == nil {
		return nil, errors.New("billing.NewEmitArchiveEventActivity: client is nil")
	}
	return &EmitArchiveEventActivity{client: client}, nil
}

// Execute calls RecordPartitionArchived on the billing app-plane service.
func (a *EmitArchiveEventActivity) Execute(ctx context.Context, in EmitInput) error {
	return a.client.RecordPartitionArchived(ctx, appclient.PartitionArchivedInput{
		Year:          in.Year,
		Month:         in.Month,
		PartitionName: in.PartitionName,
		LakeURI:       in.LakeURI,
		RowCount:      in.RowCount,
		BytesWritten:  in.BytesWritten,
	})
}
```

- [ ] **Step 4: Run tests — all pass**

```bash
go test ./plane/workflow/billing/... -run TestEmitArchiveEvent -v
```

Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/workflow/billing/emit_activity.go plane/workflow/billing/emit_activity_test.go
git commit -m "feat(workflow/billing): EmitArchiveEventActivity"
```

---

## Task 10: `ExportActivity` (TDD)

**Files:**
- Create: `plane/workflow/billing/export_activity.go`
- Create: `plane/workflow/billing/export_activity_test.go`

This is the most complex activity. It streams rows → Parquet+zstd → chunked AES-256-GCM → object store, computing SHA-256 over the encrypted stream and writing manifest + checksum alongside.

**Chunked AES-256-GCM format:** for each 4 MiB plaintext chunk: `[4-byte BE chunk_len][12-byte nonce][GCM ciphertext + 16-byte tag]`. The SHA-256 is computed over the full sequence of encoded chunks. This format decouples encryption from stream size, keeping memory usage bounded.

- [ ] **Step 1: Write the failing test**

```go
// plane/workflow/billing/export_activity_test.go
package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

func TestExportActivity_uploadsParquetAndWritesSidecarFiles(t *testing.T) {
	archiver := billingstore.NewStubArchiver()
	store := newStubObjectStore("test-bucket")
	keys := NewStubKeyProvider()

	ts := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	archiver.SetRows(2026, 5, []billingstore.UsageEventRow{
		billingstore.SeedUsageEventRow("id-1", "acc-1", ts),
		billingstore.SeedUsageEventRow("id-2", "acc-1", ts),
	})

	act, err := NewExportActivity(archiver, store, keys, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}

	result, err := act.Execute(context.Background(), ExportInput{Year: 2026, Month: 5})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.RowCount != 2 {
		t.Errorf("RowCount=%d want 2", result.RowCount)
	}
	if result.BytesWritten == 0 {
		t.Error("BytesWritten=0, expected >0")
	}
	if result.LakeURI == "" {
		t.Error("LakeURI empty")
	}
	if !strings.Contains(result.LakeURI, "year=2026/month=05") {
		t.Errorf("LakeURI=%s missing Hive prefix", result.LakeURI)
	}

	parquetKey := "billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet"
	if store.Get(parquetKey) == nil {
		t.Error("parquet file not uploaded")
	}

	manifestKey := fmt.Sprintf("%s.manifest.json", strings.TrimSuffix(parquetKey, ".parquet"))
	manifestBytes := store.Get(manifestKey)
	if manifestBytes == nil {
		t.Fatal("manifest not uploaded")
	}
	var manifest archiveManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("manifest JSON: %v", err)
	}
	if manifest.RowCount != 2 {
		t.Errorf("manifest.RowCount=%d want 2", manifest.RowCount)
	}
	if manifest.SchemaVersion != 1 {
		t.Errorf("manifest.SchemaVersion=%d want 1", manifest.SchemaVersion)
	}
	if manifest.SourcePartition != "billing.usage_events_2026_05" {
		t.Errorf("manifest.SourcePartition=%s", manifest.SourcePartition)
	}

	checksumKey := fmt.Sprintf("%s.checksum.sha256", strings.TrimSuffix(parquetKey, ".parquet"))
	if store.Get(checksumKey) == nil {
		t.Error("checksum file not uploaded")
	}
}

func TestNewExportActivity_nilDepsRejected(t *testing.T) {
	store := newStubObjectStore("b")
	keys := NewStubKeyProvider()
	archiver := billingstore.NewStubArchiver()

	if _, err := NewExportActivity(nil, store, keys, "b"); err == nil {
		t.Error("nil archiver: expected error")
	}
	if _, err := NewExportActivity(archiver, nil, keys, "b"); err == nil {
		t.Error("nil store: expected error")
	}
	if _, err := NewExportActivity(archiver, store, nil, "b"); err == nil {
		t.Error("nil keys: expected error")
	}
}
```

- [ ] **Step 2: Run — confirm compile error**

```bash
go test ./plane/workflow/billing/... -run TestExportActivity -v
```

Expected: compile error: `undefined: ExportInput`, `undefined: NewExportActivity`, `undefined: archiveManifest`.

- [ ] **Step 3: Create `export_activity.go`**

```go
// plane/workflow/billing/export_activity.go
package billing

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"github.com/parquet-go/parquet-go"
	"go.temporal.io/sdk/activity"
)

const ActivityNameExport = "billing.Export"

// ExportInput is the input to ExportActivity.Execute.
type ExportInput struct {
	Year  int
	Month int
}

// ExportResult is returned by ExportActivity.Execute.
type ExportResult struct {
	LakeURI      string
	RowCount     int64
	BytesWritten int64
	SHA256Hex    string
}

// archiveManifest is the .manifest.json written alongside each Parquet file.
type archiveManifest struct {
	SchemaVersion   int    `json:"schema_version"`
	SourcePartition string `json:"source_partition"`
	RowCount        int64  `json:"row_count"`
	BytesWritten    int64  `json:"bytes_written"`
	KEKHint         string `json:"kek_hint"`
	ArchiveTS       string `json:"archive_ts"`
	ChecksumAlg     string `json:"checksum_alg"`
}

// ExportActivity streams rows from a detached partition to the object store as
// Parquet+zstd encrypted with AES-256-GCM (chunked streaming format).
type ExportActivity struct {
	archiver billingstore.Archiver
	store    ObjectStore
	keys     KeyProvider
	bucket   string
}

// NewExportActivity returns an ExportActivity. All deps must be non-nil.
func NewExportActivity(
	archiver billingstore.Archiver,
	store ObjectStore,
	keys KeyProvider,
	bucket string,
) (*ExportActivity, error) {
	if archiver == nil {
		return nil, errors.New("billing.NewExportActivity: archiver is nil")
	}
	if store == nil {
		return nil, errors.New("billing.NewExportActivity: store is nil")
	}
	if keys == nil {
		return nil, errors.New("billing.NewExportActivity: keys is nil")
	}
	return &ExportActivity{archiver: archiver, store: store, keys: keys, bucket: bucket}, nil
}

// Execute streams the partition to the object store.
//
// Pipeline (three concurrent stages connected by io.Pipe):
//  1. Parquet goroutine: rows → parquet-go writer → plaintextW
//  2. Encrypt goroutine: plaintextR → chunked AES-256-GCM → cipherW; computes SHA-256
//  3. Main:             cipherR → ObjectStore.Upload
//
// Heartbeats every 10 000 rows so Temporal can cancel on timeout.
func (a *ExportActivity) Execute(ctx context.Context, in ExportInput) (ExportResult, error) {
	dek, err := a.keys.GetDEK(ctx, in.Year, in.Month)
	if err != nil {
		return ExportResult{}, fmt.Errorf("export: get dek: %w", err)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return ExportResult{}, fmt.Errorf("export: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return ExportResult{}, fmt.Errorf("export: new gcm: %w", err)
	}

	cursor, err := a.archiver.ScanPartitionRows(ctx, in.Year, in.Month)
	if err != nil {
		return ExportResult{}, fmt.Errorf("export: scan rows: %w", err)
	}

	// Stage 1: parquet writer → plaintextW
	plaintextR, plaintextW := io.Pipe()
	rowCountCh := make(chan int64, 1)
	writeErrCh := make(chan error, 1)
	go func() {
		defer cursor.Close()
		pw := parquet.NewGenericWriter[billingstore.UsageEventRow](plaintextW)
		var rows int64
		for cursor.Next(ctx) {
			row := cursor.Row()
			if _, werr := pw.Write([]billingstore.UsageEventRow{row}); werr != nil {
				plaintextW.CloseWithError(werr)
				writeErrCh <- werr
				return
			}
			rows++
			if rows%10000 == 0 {
				activity.RecordHeartbeat(ctx, rows)
			}
		}
		if cerr := cursor.Err(); cerr != nil {
			plaintextW.CloseWithError(cerr)
			writeErrCh <- cerr
			return
		}
		if cerr := pw.Close(); cerr != nil {
			plaintextW.CloseWithError(cerr)
			writeErrCh <- cerr
			return
		}
		plaintextW.Close()
		rowCountCh <- rows
		writeErrCh <- nil
	}()

	// Stage 2: encrypt plaintext in 4 MiB chunks → cipherW; compute SHA-256
	cipherR, cipherW := io.Pipe()
	h := sha256.New()
	var bytesWritten int64
	encErrCh := make(chan error, 1)
	go func() {
		defer cipherW.Close()
		buf := make([]byte, 4<<20) // 4 MiB
		for {
			n, readErr := io.ReadFull(plaintextR, buf)
			if n > 0 {
				nonce := make([]byte, aead.NonceSize())
				if _, rerr := rand.Read(nonce); rerr != nil {
					cipherW.CloseWithError(rerr)
					encErrCh <- rerr
					return
				}
				ct := aead.Seal(nil, nonce, buf[:n], nil)

				// frame: [4-byte BE payload_len][nonce][ciphertext]
				payloadLen := uint32(len(nonce) + len(ct))
				frame := make([]byte, 4+int(payloadLen))
				binary.BigEndian.PutUint32(frame[:4], payloadLen)
				copy(frame[4:], nonce)
				copy(frame[4+len(nonce):], ct)

				h.Write(frame)
				bytesWritten += int64(len(frame))
				if _, werr := cipherW.Write(frame); werr != nil {
					encErrCh <- werr
					return
				}
			}
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				encErrCh <- nil
				return
			}
			if readErr != nil {
				cipherW.CloseWithError(readErr)
				encErrCh <- readErr
				return
			}
		}
	}()

	// Stage 3: upload encrypted stream
	parquetKey := fmt.Sprintf(
		"billing/usage_events/year=%04d/month=%02d/usage_events_%04d_%02d.parquet",
		in.Year, in.Month, in.Year, in.Month,
	)
	uri, err := a.store.Upload(ctx, parquetKey, cipherR, -1)
	if err != nil {
		return ExportResult{}, fmt.Errorf("export: upload: %w", err)
	}

	// collect goroutine outcomes
	if werr := <-writeErrCh; werr != nil {
		return ExportResult{}, fmt.Errorf("export: parquet write: %w", werr)
	}
	if eerr := <-encErrCh; eerr != nil {
		return ExportResult{}, fmt.Errorf("export: encrypt: %w", eerr)
	}
	rowCount := <-rowCountCh
	sha256hex := fmt.Sprintf("%x", h.Sum(nil))

	// write manifest
	base := strings.TrimSuffix(parquetKey, ".parquet")
	manifest := archiveManifest{
		SchemaVersion:   1,
		SourcePartition: fmt.Sprintf("billing.usage_events_%04d_%02d", in.Year, in.Month),
		RowCount:        rowCount,
		BytesWritten:    bytesWritten,
		KEKHint:         "platform-billing-v1",
		ArchiveTS:       time.Now().UTC().Format(time.RFC3339),
		ChecksumAlg:     "sha256",
	}
	manifestJSON, _ := json.Marshal(manifest)
	if merr := a.store.PutBytes(ctx, base+".manifest.json", manifestJSON); merr != nil {
		return ExportResult{}, fmt.Errorf("export: manifest: %w", merr)
	}
	if cerr := a.store.PutBytes(ctx, base+".checksum.sha256", []byte(sha256hex)); cerr != nil {
		return ExportResult{}, fmt.Errorf("export: checksum: %w", cerr)
	}

	return ExportResult{
		LakeURI:      uri,
		RowCount:     rowCount,
		BytesWritten: bytesWritten,
		SHA256Hex:    sha256hex,
	}, nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./plane/workflow/billing/... -run TestExportActivity -v -timeout 30s
```

Expected: `TestExportActivity_uploadsParquetAndWritesSidecarFiles` PASS, `TestNewExportActivity_nilDepsRejected` PASS.

If `parquet.NewGenericWriter` API differs from the installed version, check `go doc github.com/parquet-go/parquet-go` and adjust the constructor call.

- [ ] **Step 5: Commit**

```bash
git add plane/workflow/billing/export_activity.go plane/workflow/billing/export_activity_test.go
git commit -m "feat(workflow/billing): ExportActivity — Parquet+zstd + AES-256-GCM stream"
```

---

## Task 11: `PartitionArchiveWorkflow` (TDD)

**Files:**
- Create: `plane/workflow/billing/archive_workflow.go`
- Create: `plane/workflow/billing/archive_workflow_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// plane/workflow/billing/archive_workflow_test.go
package billing

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestPartitionArchiveWorkflow_happyPath(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(PartitionArchiveWorkflow)

	// Mock all four activities in sequence.
	env.OnActivity(ActivityNameDetachPartition, mock.Anything, DetachInput{Year: 2026, Month: 5}).
		Return(nil)
	env.OnActivity(ActivityNameExport, mock.Anything, ExportInput{Year: 2026, Month: 5}).
		Return(ExportResult{
			LakeURI:      "s3://test-bucket/billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet",
			RowCount:     100,
			BytesWritten: 4096,
			SHA256Hex:    "abc123",
		}, nil)
	env.OnActivity(ActivityNameEmitArchiveEvent, mock.Anything, EmitInput{
		Year:          2026,
		Month:         5,
		PartitionName: "billing.usage_events_2026_05",
		LakeURI:       "s3://test-bucket/billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet",
		RowCount:      100,
		BytesWritten:  4096,
	}).Return(nil)
	env.OnActivity(ActivityNameDropPartition, mock.Anything, DropInput{Year: 2026, Month: 5}).
		Return(nil)

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
	if result.RowCount != 100 {
		t.Errorf("RowCount=%d want 100", result.RowCount)
	}
	if result.LakeURI == "" {
		t.Error("LakeURI empty")
	}
}

func TestPartitionArchiveWorkflow_exportRetryOnFirstFailure(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(PartitionArchiveWorkflow)

	exportResult := ExportResult{
		LakeURI:      "s3://test-bucket/billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet",
		RowCount:     50,
		BytesWritten: 2048,
		SHA256Hex:    "def456",
	}
	env.OnActivity(ActivityNameDetachPartition, mock.Anything, DetachInput{Year: 2026, Month: 5}).
		Return(nil)
	// Export fails first call, succeeds second.
	env.OnActivity(ActivityNameExport, mock.Anything, ExportInput{Year: 2026, Month: 5}).
		Return(ExportResult{}, errors.New("s3: connection reset")).Once()
	env.OnActivity(ActivityNameExport, mock.Anything, ExportInput{Year: 2026, Month: 5}).
		Return(exportResult, nil)
	env.OnActivity(ActivityNameEmitArchiveEvent, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(ActivityNameDropPartition, mock.Anything, DropInput{Year: 2026, Month: 5}).
		Return(nil)

	env.ExecuteWorkflow(PartitionArchiveWorkflow, ArchiveInput{
		RunTime: time.Date(2027, 11, 24, 14, 0, 0, 0, time.UTC),
		Year:    2026,
		Month:   5,
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error after retry: %v", err)
	}
	var result ArchiveResult
	_ = env.GetWorkflowResult(&result)
	if result.RowCount != 50 {
		t.Errorf("RowCount=%d want 50", result.RowCount)
	}
}

func TestPartitionArchiveWorkflow_dropFailureSurfacesError(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(PartitionArchiveWorkflow)

	env.OnActivity(ActivityNameDetachPartition, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(ActivityNameExport, mock.Anything, mock.Anything).
		Return(ExportResult{LakeURI: "s3://b/k", RowCount: 1, BytesWritten: 100, SHA256Hex: "x"}, nil)
	env.OnActivity(ActivityNameEmitArchiveEvent, mock.Anything, mock.Anything).Return(nil)
	// Drop fails exhausting retries.
	env.OnActivity(ActivityNameDropPartition, mock.Anything, mock.Anything).
		Return(errors.New("pg: table does not exist"))

	env.ExecuteWorkflow(PartitionArchiveWorkflow, ArchiveInput{
		RunTime: time.Date(2027, 11, 24, 14, 0, 0, 0, time.UTC),
		Year:    2026,
		Month:   5,
	})

	if env.GetWorkflowError() == nil {
		t.Error("expected workflow error when drop fails, got nil")
	}
}

// archiveActivityOpts mirrors the real workflow options so test assertions match.
func archiveActivityOpts() workflow.ActivityOptions {
	return workflow.ActivityOptions{}
}
```

- [ ] **Step 2: Add `testify/mock` import**

The Temporal test suite uses its own mock system. Replace `mock.Anything` with the Temporal equivalent. The Temporal `testsuite` package uses `mock.Anything` from `github.com/stretchr/testify/mock`. Check that testify is already a dependency:

```bash
grep "testify" go.mod
```

If absent:

```bash
go get github.com/stretchr/testify@latest
```

- [ ] **Step 3: Run — confirm compile error**

```bash
go test ./plane/workflow/billing/... -run TestPartitionArchiveWorkflow -v
```

Expected: compile error: `undefined: PartitionArchiveWorkflow`, `undefined: ArchiveInput`, `undefined: ArchiveResult`.

- [ ] **Step 4: Create `archive_workflow.go`**

```go
// plane/workflow/billing/archive_workflow.go
package billing

import (
	"fmt"
	"time"

	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/workflow"
)

// ArchiveInput is the input to PartitionArchiveWorkflow.
// Year and Month identify the partition to archive.
// RunTime is the schedule's scheduled-time — passed through for determinism but
// not used to derive Year/Month here (caller sets them explicitly).
type ArchiveInput struct {
	RunTime time.Time
	Year    int
	Month   int
}

// ArchiveResult is the output of PartitionArchiveWorkflow.
type ArchiveResult struct {
	PartitionName string
	LakeURI       string
	RowCount      int64
	BytesWritten  int64
}

// PartitionArchiveWorkflow archives billing.usage_events_YYYY_MM to the
// analytics-lake object store and drops the partition from PostgreSQL.
//
// Activity sequence: DetachPartition → Export → EmitOutbox → DropPartition.
// Drop failure after emit surfaces as a workflow error — data exists in both
// PG (detached) and object store; no data loss. Runbook: verify object store
// integrity then DROP TABLE manually or re-run the workflow.
func PartitionArchiveWorkflow(ctx workflow.Context, in ArchiveInput) (ArchiveResult, error) {
	partitionName := fmt.Sprintf("billing.usage_events_%04d_%02d", in.Year, in.Month)

	shortOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * 60 * time.Second, // 5 minutes
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	longOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 4 * 60 * 60 * time.Second, // 4 hours
		HeartbeatTimeout:    5 * 60 * time.Second,       // 5 minutes
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}

	// 1. Detach partition from parent table.
	detachCtx := workflow.WithActivityOptions(ctx, shortOpts)
	if err := workflow.ExecuteActivity(detachCtx, ActivityNameDetachPartition,
		DetachInput{Year: in.Year, Month: in.Month},
	).Get(detachCtx, nil); err != nil {
		return ArchiveResult{}, fmt.Errorf("archive: detach: %w", err)
	}

	// 2. Stream rows to object store as Parquet+zstd+AES-256-GCM.
	exportCtx := workflow.WithActivityOptions(ctx, longOpts)
	var exportResult ExportResult
	if err := workflow.ExecuteActivity(exportCtx, ActivityNameExport,
		ExportInput{Year: in.Year, Month: in.Month},
	).Get(exportCtx, &exportResult); err != nil {
		return ArchiveResult{}, fmt.Errorf("archive: export: %w", err)
	}

	// 3. Emit billing.partition_archived via app-plane (ADR-019).
	emitCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Second,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	})
	if err := workflow.ExecuteActivity(emitCtx, ActivityNameEmitArchiveEvent,
		EmitInput{
			Year:          in.Year,
			Month:         in.Month,
			PartitionName: partitionName,
			LakeURI:       exportResult.LakeURI,
			RowCount:      exportResult.RowCount,
			BytesWritten:  exportResult.BytesWritten,
		},
	).Get(emitCtx, nil); err != nil {
		return ArchiveResult{}, fmt.Errorf("archive: emit: %w", err)
	}

	// 4. Drop the detached partition. Only reached after export + emit succeed.
	// If this fails, data exists in both PG and object store — no data loss.
	dropCtx := workflow.WithActivityOptions(ctx, shortOpts)
	if err := workflow.ExecuteActivity(dropCtx, ActivityNameDropPartition,
		DropInput{Year: in.Year, Month: in.Month},
	).Get(dropCtx, nil); err != nil {
		return ArchiveResult{}, fmt.Errorf("archive: drop (data safe in object store): %w", err)
	}

	return ArchiveResult{
		PartitionName: partitionName,
		LakeURI:       exportResult.LakeURI,
		RowCount:      exportResult.RowCount,
		BytesWritten:  exportResult.BytesWritten,
	}, nil
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./plane/workflow/billing/... -run TestPartitionArchiveWorkflow -v
```

Expected: all 3 tests PASS.

- [ ] **Step 6: Run full billing package tests to confirm no regressions**

```bash
go test ./plane/workflow/billing/... -v
```

Expected: all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add plane/workflow/billing/archive_workflow.go plane/workflow/billing/archive_workflow_test.go
git commit -m "feat(workflow/billing): PartitionArchiveWorkflow — detach→export→emit→drop"
```

---

## Task 12: Schedule + bundle wiring

**Files:**
- Create: `plane/workflow/billing/archive_schedules.go`
- Modify: `plane/workflow/billing/bundle.go`

- [ ] **Step 1: Create `archive_schedules.go`**

```go
// plane/workflow/billing/archive_schedules.go
package billing

import (
	"context"

	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/client"
)

// ArchiveScheduleID is the stable Temporal schedule ID for the monthly archive.
const ArchiveScheduleID = "billing-partition-archive"

// ArchiveCronExpression fires at 14:00 UTC on the 24th — 2 hours after the
// rollover schedule (12:00 UTC same day), giving rollover time to complete.
const ArchiveCronExpression = "0 14 24 * *"

// EnsureArchiveSchedule registers (or converges) the monthly archive schedule.
// Called by cmd/workflow-worker at boot after the worker is running.
// The schedule passes its own scheduled-time as RunTime for deterministic replay.
func EnsureArchiveSchedule(ctx context.Context, sc gswf.ScheduleClient) (client.ScheduleHandle, error) {
	return gswf.EnsureSchedule(ctx, sc, client.ScheduleOptions{
		ID: ArchiveScheduleID,
		Spec: client.ScheduleSpec{
			CronExpressions: []string{ArchiveCronExpression},
			TimeZoneName:    "UTC",
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        "billing-partition-archive",
			Workflow:  "PartitionArchiveWorkflow",
			TaskQueue: gswf.QueueBillingMaintenance,
		},
	})
}
```

- [ ] **Step 2: Update `bundle.go` to register archive workflow and activities**

Read the current bundle.go before editing:

Current content:
```go
package billing

import (
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

func Bundle(activity *CreatePartitionActivity) gswf.Bundle {
	return gswf.Bundle{
		TaskQueue:  gswf.QueueBillingMaintenance,
		Workflows:  []any{PartitionRolloverWorkflow},
		Activities: []any{gswf.NamedActivity{Name: ActivityNameCreatePartition, Activity: activity.Execute}},
	}
}
```

Replace with:

```go
// plane/workflow/billing/bundle.go
package billing

import (
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

// ArchiveDeps holds the dependencies injected into the archive activities.
// Constructed in cmd/workflow-worker and passed to Bundle.
type ArchiveDeps struct {
	Detach *DetachPartitionActivity
	Export *ExportActivity
	Emit   *EmitArchiveEventActivity
	Drop   *DropPartitionActivity
}

// Bundle returns the registration set for the billing-maintenance task queue.
// Hosts both the partition-rollover (#18-rollover) and archive (#69) workflows.
func Bundle(rollover *CreatePartitionActivity, archive ArchiveDeps) gswf.Bundle {
	return gswf.Bundle{
		TaskQueue: gswf.QueueBillingMaintenance,
		Workflows: []any{
			PartitionRolloverWorkflow,
			PartitionArchiveWorkflow,
		},
		Activities: []any{
			gswf.NamedActivity{Name: ActivityNameCreatePartition, Activity: rollover.Execute},
			gswf.NamedActivity{Name: ActivityNameDetachPartition, Activity: archive.Detach.Execute},
			gswf.NamedActivity{Name: ActivityNameExport, Activity: archive.Export.Execute},
			gswf.NamedActivity{Name: ActivityNameEmitArchiveEvent, Activity: archive.Emit.Execute},
			gswf.NamedActivity{Name: ActivityNameDropPartition, Activity: archive.Drop.Execute},
		},
	}
}
```

- [ ] **Step 3: Build — verify no compile errors**

```bash
go build ./...
```

Expected: exit 0. Note: `cmd/workflow-worker` may fail to compile if it calls `Bundle` with the old signature — fix by updating the call site to pass a zero `ArchiveDeps{}` (stubs) until real deps are wired:

Find the call site:

```bash
grep -rn "billing.Bundle" --include="*.go" .
```

Update the call site to match the new signature, using stub values for archive deps that are not yet wired:

```go
// Temporary: archive deps stubbed until full wiring in a follow-up.
archiveDeps := billing.ArchiveDeps{}
bundle := billing.Bundle(rolloverActivity, archiveDeps)
```

- [ ] **Step 4: Run all tests**

```bash
go test ./...
```

Expected: all existing tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/workflow/billing/archive_schedules.go plane/workflow/billing/bundle.go
git commit -m "feat(workflow/billing): EnsureArchiveSchedule + bundle wiring"
```

---

## Task 13: Add minio to docker-compose.yml

**Files:**
- Modify: `docker-compose.yml`

- [ ] **Step 1: Add minio service**

Open `docker-compose.yml`. After the `redis` service block (before `zookeeper`), add:

```yaml
  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: gitscale
      MINIO_ROOT_PASSWORD: gitscale
    ports:
      - "9000:9000"   # S3 API — configure S3_ENDPOINT=http://localhost:9000
      - "9001:9001"   # web console — http://localhost:9001
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:9000/minio/health/live"]
      interval: 5s
      timeout: 5s
      retries: 5
```

- [ ] **Step 2: Verify docker-compose parses**

```bash
docker compose config --quiet
```

Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
git add docker-compose.yml
git commit -m "chore(dev): add minio service to docker-compose for local archive dev"
```

**Local dev env vars for workflow worker:**

```
S3_ENDPOINT=http://localhost:9000
S3_BUCKET=gitscale-analytics-local
AWS_ACCESS_KEY_ID=gitscale
AWS_SECRET_ACCESS_KEY=gitscale
AWS_REGION=us-east-1
```

---

## Task 14: Final build + test pass

- [ ] **Step 1: Build everything**

```bash
go build ./...
```

Expected: exit 0.

- [ ] **Step 2: Run full test suite with race detector**

```bash
go test -race ./...
```

Expected: all tests PASS, 0 race conditions.

- [ ] **Step 3: Run determinism lint**

```bash
make lint-determinism
```

Expected: clean (no non-deterministic patterns in workflow code).

- [ ] **Step 4: Final commit if any fixes were needed**

```bash
git add -p  # review any remaining changes
git commit -m "fix(workflow/billing): address review findings"
```

---

## Follow-up issues to file in PR description

Open GitHub issues for deferred scope:

1. `[Application] BillingClient gRPC impl + billing app-plane service (RecordPartitionArchived)` — plane/application
2. `[Data] Glue Data Catalog registration activity for billing archive` — plane/data
3. `[Workflow] KeyProvider Vault HKDF wiring for billing archive DEK derivation` — plane/workflow
4. `[Workflow] RestorePartition workflow (acknowledged in ADR-018)` — plane/workflow
5. `[Workflow] Per-month DEK destruction workflow (post-7y retention enforcement)` — plane/workflow
