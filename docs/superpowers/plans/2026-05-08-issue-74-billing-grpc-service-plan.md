# Issue #74 BillingClient gRPC + billing service — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `StubBillingClient` with a real gRPC client backed by a new `cmd/billing-service` app-plane binary that writes a source row into `billing.partition_archives` and a `billing.partition_archived` event into `billing.billing_outbox` in the same Tx (ADR-008, ADR-019).

**Architecture:** Mirror `plane/application/identity/` exactly. Add `billing` domain handles to `store.MetadataStore` / `store.Tx`. Service uses `store.Transact` + `tx.WriteOutbox(DomainBilling, …)`. Idempotency via `UNIQUE(year,month,partition_name)` + `INSERT ... ON CONFLICT DO NOTHING RETURNING id`; outbox row written only on first insert.

**Tech Stack:** Go 1.22, pgx/v5, gRPC, buf, testcontainers-go, google/uuid.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-74-billing-grpc-service-design.md`

**Branch:** `feat/application-billing-grpc-service` (worktree: `../gitscale.worktrees/feat-application-billing-grpc-service`)

---

## File map

### Create
- `plane/data/migrations/006_billing_partition_archives.sql` — table + index
- `plane/data/store/billing/` — `partition_archives.go` (model), `partition_archives_postgres.go`, `partition_archives_stub.go`, `partition_archives_postgres_test.go`
- `plane/application/billing/doc.go` — package doc, ADR refs
- `plane/application/billing/models.go` — `PartitionArchive` aliased from store
- `plane/application/billing/events.go` — `EventTypePartitionArchived` + payload struct
- `plane/application/billing/service.go` — `Service` interface, sentinel errors
- `plane/application/billing/postgres_service.go` — `PostgresService`
- `plane/application/billing/postgres_service_test.go` — integration-tagged Postgres tests
- `plane/application/billing/stub_service.go` — in-memory impl
- `plane/application/billing/stub_service_test.go` — unit tests
- `plane/application/billing/grpc_server.go` — gRPC adapter
- `plane/application/billing/grpc_server_test.go` — gRPC unit (StubService + bufconn)
- `plane/application/billing/integration_test.go` — full path through bufconn + PG
- `internal/proto/gitscale/billing/v1/billing.proto` — RPC definition
- `internal/proto/gitscale/billing/v1/billing.pb.go` — generated (buf)
- `internal/proto/gitscale/billing/v1/billing_grpc.pb.go` — generated (buf)
- `cmd/billing-service/main.go` — binary
- `cmd/billing-service/integration_test.go` — boots binary, ensures gRPC reachable
- `plane/workflow/appclient/billing_grpc.go` — gRPC client adapter

### Modify
- `plane/data/store/metadata.go` — add `Billing()` to both `MetadataStore` and `Tx`; add `BillingReader` / `BillingWriter` interfaces; add `PartitionArchive` model
- `plane/data/store/postgres/store.go` — wire `Billing()` returning a postgres impl on tx + reader
- `plane/data/store/stub/store.go` (or equivalent stub aggregate) — wire stub
- `plane/data/store/postgres/compliance_test.go` — extend migrations list to include `006_billing_partition_archives.sql`

### Untouched (intentionally — out of scope)
- `cmd/workflow-worker/main.go` — wiring of new client into worker is issue #76
- `plane/workflow/archive/` — stays on `StubBillingClient` for unit tests
- `plane/workflow/appclient/billing.go` — keeps existing `StubBillingClient` and `BillingClient` interface

---

## Pre-flight (do once before Task 1)

- [ ] **Step P.1: Create worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
mkdir -p /home/mitta/clients/gitscale/repos/gitscale.worktrees
git worktree add -b feat/application-billing-grpc-service \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-billing-grpc-service \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-billing-grpc-service
git status --porcelain
```

Expected: clean worktree on new branch.

- [ ] **Step P.2: Verify existing code compiles + tests pass on baseline**

```bash
go build ./...
go vet ./...
go test ./plane/application/identity/... -count=1
```

Expected: zero failures. If anything fails, stop — baseline is broken, do not continue.

---

## Task 1: SQL migration for `billing.partition_archives`

**Files:**
- Create: `plane/data/migrations/006_billing_partition_archives.sql`
- Modify: `plane/data/store/postgres/compliance_test.go` (add migration to list)

- [ ] **Step 1.1: Write the migration**

```sql
-- 006_billing_partition_archives.sql
-- Source-of-record for completed monthly partition archives.
-- ADR-008 outbox pattern: paired with billing.billing_outbox row written
-- in the same Tx by plane/application/billing.PostgresService.

BEGIN;

CREATE TABLE billing.partition_archives (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  year            SMALLINT NOT NULL CHECK (year BETWEEN 2026 AND 2100),
  month           SMALLINT NOT NULL CHECK (month BETWEEN 1 AND 12),
  partition_name  TEXT     NOT NULL,
  lake_uri        TEXT     NOT NULL,
  row_count       BIGINT   NOT NULL CHECK (row_count >= 0),
  bytes_written   BIGINT   NOT NULL CHECK (bytes_written >= 0),
  archived_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (year, month, partition_name)
);

COMMIT;
```

- [ ] **Step 1.2: Add migration to compliance test list**

In `plane/data/store/postgres/compliance_test.go` find the `migrations := []string{...}` slice that lists the SQL files in order and append `"006_billing_partition_archives.sql"`.

- [ ] **Step 1.3: Run compliance test**

```bash
go test -tags integration ./plane/data/store/postgres/... -run TestSchemaCompliance -count=1
```

Expected: PASS. Migration applies cleanly to a fresh testcontainer Postgres.

- [ ] **Step 1.4: Commit**

```bash
git add plane/data/migrations/006_billing_partition_archives.sql \
        plane/data/store/postgres/compliance_test.go
git commit -m "$(cat <<'EOF'
feat(data): billing.partition_archives table for #74 outbox source row

UNIQUE(year, month, partition_name) anchors idempotent retry from
EmitArchiveEventActivity; the paired billing.billing_outbox row is
written in the same Tx by the upcoming application/billing service.

Refs ADR-008.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Storage layer — `BillingReader` / `BillingWriter` + postgres impl + stub

**Files:**
- Modify: `plane/data/store/metadata.go`
- Create: `plane/data/store/billing/partition_archives.go` (model only — keep package types focused)

Note: the existing `plane/data/store/billing/` package already holds the
partitioner/archiver. Add the new types alongside; do not refactor unrelated
code.

- [ ] **Step 2.1: Add `PartitionArchive` model and Billing reader/writer interfaces in metadata.go**

Append to `plane/data/store/metadata.go` (after the `RepositoryWriter` block):

```go
// PartitionArchive is the billing.partition_archives row model.
type PartitionArchive struct {
	ID            uuid.UUID
	Year          int
	Month         int
	PartitionName string
	LakeURI       string
	RowCount      int64
	BytesWritten  int64
	ArchivedAt    time.Time
}

// BillingReader exposes read-only queries against the billing domain.
type BillingReader interface {
	GetPartitionArchiveByKey(ctx context.Context, year, month int, partitionName string) (*PartitionArchive, error)
}

// BillingWriter exposes write operations against the billing domain.
// Methods must be called within a Tx.
type BillingWriter interface {
	BillingReader
	// InsertPartitionArchiveIfAbsent attempts to insert pa. It returns
	// (id, true, nil) if the row was inserted, or (existingID, false, nil)
	// on UNIQUE-conflict. Errors other than UNIQUE conflict are returned.
	InsertPartitionArchiveIfAbsent(ctx context.Context, pa PartitionArchive) (uuid.UUID, bool, error)
}
```

Then extend `MetadataStore` and `Tx`:

```go
type MetadataStore interface {
	Transact(ctx context.Context, fn func(Tx) error) error
	Identity() IdentityReader
	Repositories() RepositoryReader
	Billing() BillingReader
}

type Tx interface {
	Identity() IdentityWriter
	Repositories() RepositoryWriter
	Billing() BillingWriter
	WriteOutbox(ctx context.Context, domain Domain, aggregateType string, aggregateID uuid.UUID, eventType string, payload any) error
}
```

- [ ] **Step 2.2: Build to surface every implementer that now lacks `Billing()`**

```bash
go build ./...
```

Expected: compile errors at every store implementation (postgres + stub) saying `*store missing method Billing`.

- [ ] **Step 2.3: Find each implementer**

```bash
grep -rln "func .*MetadataStore" plane/data/store/
grep -rln "func .*Tx)" plane/data/store/
```

Note paths to the postgres `MetadataStore` impl and any stub (compliance test stub or integration helper).

- [ ] **Step 2.4: Implement BillingReader on postgres metadata store**

Add (in the postgres store file alongside `Identity()` / `Repositories()`):

```go
func (s *PostgresStore) Billing() store.BillingReader {
	return &billingReader{pool: s.pool}
}

type billingReader struct {
	pool *pgxpool.Pool
}

func (r *billingReader) GetPartitionArchiveByKey(ctx context.Context, year, month int, partitionName string) (*store.PartitionArchive, error) {
	const q = `
SELECT id, year, month, partition_name, lake_uri, row_count, bytes_written, archived_at
FROM billing.partition_archives
WHERE year = $1 AND month = $2 AND partition_name = $3`
	row := r.pool.QueryRow(ctx, q, year, month, partitionName)
	var pa store.PartitionArchive
	if err := row.Scan(&pa.ID, &pa.Year, &pa.Month, &pa.PartitionName, &pa.LakeURI, &pa.RowCount, &pa.BytesWritten, &pa.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("billing: get partition archive: %w", err)
	}
	return &pa, nil
}
```

(Adapt struct/field names to whatever the existing postgres store uses; the
field accessing the pool may be named differently.)

- [ ] **Step 2.5: Implement BillingWriter on postgres Tx**

Add to the postgres `Tx` impl:

```go
func (t *postgresTx) Billing() store.BillingWriter {
	return &billingTxWriter{tx: t.tx}
}

type billingTxWriter struct {
	tx pgx.Tx
}

// Embed read methods.
func (w *billingTxWriter) GetPartitionArchiveByKey(ctx context.Context, year, month int, partitionName string) (*store.PartitionArchive, error) {
	const q = `
SELECT id, year, month, partition_name, lake_uri, row_count, bytes_written, archived_at
FROM billing.partition_archives
WHERE year = $1 AND month = $2 AND partition_name = $3`
	row := w.tx.QueryRow(ctx, q, year, month, partitionName)
	var pa store.PartitionArchive
	if err := row.Scan(&pa.ID, &pa.Year, &pa.Month, &pa.PartitionName, &pa.LakeURI, &pa.RowCount, &pa.BytesWritten, &pa.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("billing: get partition archive (tx): %w", err)
	}
	return &pa, nil
}

func (w *billingTxWriter) InsertPartitionArchiveIfAbsent(ctx context.Context, pa store.PartitionArchive) (uuid.UUID, bool, error) {
	const q = `
INSERT INTO billing.partition_archives
       (id, year, month, partition_name, lake_uri, row_count, bytes_written, archived_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (year, month, partition_name) DO NOTHING
RETURNING id`
	var id uuid.UUID
	err := w.tx.QueryRow(ctx, q, pa.ID, pa.Year, pa.Month, pa.PartitionName, pa.LakeURI, pa.RowCount, pa.BytesWritten, pa.ArchivedAt).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("billing: insert partition archive: %w", err)
	}
	// UNIQUE conflict (ON CONFLICT DO NOTHING returns no row) — read existing id.
	existing, gerr := w.GetPartitionArchiveByKey(ctx, pa.Year, pa.Month, pa.PartitionName)
	if gerr != nil {
		return uuid.Nil, false, gerr
	}
	if existing == nil {
		return uuid.Nil, false, fmt.Errorf("billing: conflict but row not found")
	}
	return existing.ID, false, nil
}
```

- [ ] **Step 2.6: Implement Billing on stub store**

In the stub `MetadataStore` and stub `Tx`, add a `Billing()` returning a stub
that stores rows in a `map[string]store.PartitionArchive` keyed by
`fmt.Sprintf("%d-%d-%s", year, month, partitionName)`.

`InsertPartitionArchiveIfAbsent`:
- if key exists, return `(existing.ID, false, nil)`.
- else store a copy with `pa.ID` and return `(pa.ID, true, nil)`.

`GetPartitionArchiveByKey`: return pointer to copy or `nil` if absent.

- [ ] **Step 2.7: Compile**

```bash
go build ./...
```

Expected: success.

- [ ] **Step 2.8: Postgres test for the new store layer (testcontainer)**

Create `plane/data/store/billing/partition_archives_postgres_test.go` (build tag `integration`):

```go
//go:build integration

package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	storetest "github.com/gitscale-platform/gitscale/plane/data/store/postgres/postgrestest"
	"github.com/google/uuid"
)

func TestInsertPartitionArchiveIfAbsent_FirstAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	ms := storepg.New(pool)

	pa := store.PartitionArchive{
		ID:            uuid.New(),
		Year:          2026,
		Month:         5,
		PartitionName: "usage_events_2026_05",
		LakeURI:       "s3://lake/billing/usage_events/2026/05/",
		RowCount:      100,
		BytesWritten:  1024,
		ArchivedAt:    time.Now().UTC(),
	}

	var firstID uuid.UUID
	if err := ms.Transact(ctx, func(tx store.Tx) error {
		id, created, err := tx.Billing().InsertPartitionArchiveIfAbsent(ctx, pa)
		if err != nil {
			return err
		}
		if !created {
			t.Fatalf("expected created=true on first insert")
		}
		firstID = id
		return nil
	}); err != nil {
		t.Fatalf("first tx: %v", err)
	}

	// Idempotent retry — different attempt UUID, same natural key.
	pa2 := pa
	pa2.ID = uuid.New()
	pa2.LakeURI = "s3://lake/billing/usage_events/2026/05/RETRY"
	if err := ms.Transact(ctx, func(tx store.Tx) error {
		id, created, err := tx.Billing().InsertPartitionArchiveIfAbsent(ctx, pa2)
		if err != nil {
			return err
		}
		if created {
			t.Fatalf("expected created=false on idempotent retry")
		}
		if id != firstID {
			t.Fatalf("expected returned id %s, got %s", firstID, id)
		}
		return nil
	}); err != nil {
		t.Fatalf("retry tx: %v", err)
	}
}
```

(If `postgrestest.NewPool(t)` does not exist in the repo, find the helper used
by `plane/data/store/postgres/compliance_test.go` and reuse it.)

- [ ] **Step 2.9: Run the test**

```bash
go test -tags integration -race -run TestInsertPartitionArchiveIfAbsent ./plane/data/store/billing/...
```

Expected: PASS.

- [ ] **Step 2.10: Commit**

```bash
git add plane/data/store/...
git commit -m "$(cat <<'EOF'
feat(data): add Billing reader/writer for partition_archives (#74)

Adds tx.Billing().InsertPartitionArchiveIfAbsent which encapsulates the
ON CONFLICT DO NOTHING semantics that anchor outbox idempotency for the
RecordPartitionArchived RPC.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Proto definition + buf generate

**Files:**
- Create: `internal/proto/gitscale/billing/v1/billing.proto`
- Create (generated): `internal/proto/gitscale/billing/v1/billing.pb.go`, `internal/proto/gitscale/billing/v1/billing_grpc.pb.go`

- [ ] **Step 3.1: Write the proto**

```proto
// Billing service gRPC surface (ADR-019). RecordPartitionArchived is the only
// RPC at this revision; it maps onto plane/application/billing.Service and
// performs source-row + outbox-row write in a single Tx (ADR-008).
syntax = "proto3";

package gitscale.billing.v1;

option go_package = "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1;billingv1";

service BillingService {
  rpc RecordPartitionArchived(RecordPartitionArchivedRequest)
      returns (RecordPartitionArchivedResponse);
}

message RecordPartitionArchivedRequest {
  int32  year           = 1;  // 2026..
  int32  month          = 2;  // 1..12
  string partition_name = 3;  // e.g. "usage_events_2026_05"
  string lake_uri       = 4;  // canonical s3:// URI
  int64  row_count      = 5;
  int64  bytes_written  = 6;
}

message RecordPartitionArchivedResponse {
  string archive_id = 1;  // UUID of the partition_archives row (new or existing)
  bool   created    = 2;  // false on idempotent retry
}
```

- [ ] **Step 3.2: Generate**

```bash
make proto
```

(or `buf generate` if `make proto` is not the right target — check `Makefile`)

Expected: `internal/proto/gitscale/billing/v1/billing.pb.go` and `…billing_grpc.pb.go` are produced.

- [ ] **Step 3.3: Compile**

```bash
go build ./...
go vet ./...
```

Expected: success.

- [ ] **Step 3.4: Commit**

```bash
git add internal/proto/gitscale/billing/
git commit -m "$(cat <<'EOF'
feat(proto): BillingService.RecordPartitionArchived (#74)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Application service — interface + stub + tests

**Files:**
- Create: `plane/application/billing/doc.go`
- Create: `plane/application/billing/models.go`
- Create: `plane/application/billing/events.go`
- Create: `plane/application/billing/service.go`
- Create: `plane/application/billing/stub_service.go`
- Create: `plane/application/billing/stub_service_test.go`

- [ ] **Step 4.1: doc.go**

```go
// Package billing is the application-plane billing domain service. It exposes
// state-mutating operations (currently RecordPartitionArchived) that perform
// source-row + outbox-row writes in a single transaction (ADR-008). Workflow-
// plane callers reach this package over gRPC via plane/workflow/appclient
// (ADR-019); only the application plane talks to it in-process.
package billing
```

- [ ] **Step 4.2: models.go**

```go
package billing

import "github.com/gitscale-platform/gitscale/plane/data/store"

// PartitionArchive is the application-layer view of a billing.partition_archives row.
// Aliased to the storage struct; same convention as identity.HumanUser.
type PartitionArchive = store.PartitionArchive

// RecordPartitionArchivedInput is the service-level input for the same-named
// method. It is the shape used by both the in-process call and the gRPC entry
// point (after proto → struct translation).
type RecordPartitionArchivedInput struct {
	Year          int
	Month         int
	PartitionName string
	LakeURI       string
	RowCount      int64
	BytesWritten  int64
}

// RecordPartitionArchivedOutput is the service-level output.
type RecordPartitionArchivedOutput struct {
	ArchiveID string // UUID stringified
	Created   bool   // false on idempotent retry
}
```

- [ ] **Step 4.3: events.go**

```go
package billing

import (
	"time"

	"github.com/google/uuid"
)

// EventTypePartitionArchived is the event_type written to billing.billing_outbox.
const EventTypePartitionArchived = "billing.partition_archived"

const envelopeVersion = 1

// PartitionArchivedPayload is the JSON payload written to the outbox.
type PartitionArchivedPayload struct {
	ArchiveID       uuid.UUID `json:"archive_id"`
	Year            int       `json:"year"`
	Month           int       `json:"month"`
	PartitionName   string    `json:"partition_name"`
	LakeURI         string    `json:"lake_uri"`
	RowCount        int64     `json:"row_count"`
	BytesWritten    int64     `json:"bytes_written"`
	ArchivedAt      time.Time `json:"archived_at"`
	EnvelopeVersion int       `json:"_envelope_version"`
}

func newPartitionArchivedPayload(pa PartitionArchive) PartitionArchivedPayload {
	return PartitionArchivedPayload{
		ArchiveID:       pa.ID,
		Year:            pa.Year,
		Month:           pa.Month,
		PartitionName:   pa.PartitionName,
		LakeURI:         pa.LakeURI,
		RowCount:        pa.RowCount,
		BytesWritten:    pa.BytesWritten,
		ArchivedAt:      pa.ArchivedAt,
		EnvelopeVersion: envelopeVersion,
	}
}
```

- [ ] **Step 4.4: service.go**

```go
package billing

import (
	"context"
	"errors"
)

// Service is the billing domain service. RecordPartitionArchived performs
// source-row + outbox-row writes in a single Tx (ADR-008). State mutations
// are exclusive to this service in the application plane; the workflow
// plane reaches it through the gRPC surface (ADR-019).
type Service interface {
	RecordPartitionArchived(ctx context.Context, in RecordPartitionArchivedInput) (RecordPartitionArchivedOutput, error)
}

// Sentinel errors.
var (
	ErrInvalidYear          = errors.New("billing: year out of range (2026..2100)")
	ErrInvalidMonth         = errors.New("billing: month out of range (1..12)")
	ErrEmptyPartitionName   = errors.New("billing: partition_name is empty")
	ErrEmptyLakeURI         = errors.New("billing: lake_uri is empty")
	ErrNegativeCount        = errors.New("billing: row_count or bytes_written is negative")
)

func validateInput(in RecordPartitionArchivedInput) error {
	if in.Year < 2026 || in.Year > 2100 {
		return ErrInvalidYear
	}
	if in.Month < 1 || in.Month > 12 {
		return ErrInvalidMonth
	}
	if in.PartitionName == "" {
		return ErrEmptyPartitionName
	}
	if in.LakeURI == "" {
		return ErrEmptyLakeURI
	}
	if in.RowCount < 0 || in.BytesWritten < 0 {
		return ErrNegativeCount
	}
	return nil
}
```

- [ ] **Step 4.5: stub_service.go**

```go
package billing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// StubService is an in-memory Service for unit tests. It enforces the same
// idempotency contract as PostgresService.
type StubService struct {
	mu    sync.Mutex
	rows  map[string]PartitionArchive
	clock func() time.Time
}

// NewStubService returns an empty StubService.
func NewStubService() *StubService {
	return &StubService{
		rows:  map[string]PartitionArchive{},
		clock: func() time.Time { return time.Now().UTC() },
	}
}

func (s *StubService) RecordPartitionArchived(_ context.Context, in RecordPartitionArchivedInput) (RecordPartitionArchivedOutput, error) {
	if err := validateInput(in); err != nil {
		return RecordPartitionArchivedOutput{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%d-%d-%s", in.Year, in.Month, in.PartitionName)
	if existing, ok := s.rows[key]; ok {
		return RecordPartitionArchivedOutput{ArchiveID: existing.ID.String(), Created: false}, nil
	}
	pa := PartitionArchive{
		ID:            uuid.New(),
		Year:          in.Year,
		Month:         in.Month,
		PartitionName: in.PartitionName,
		LakeURI:       in.LakeURI,
		RowCount:      in.RowCount,
		BytesWritten:  in.BytesWritten,
		ArchivedAt:    s.clock(),
	}
	s.rows[key] = pa
	return RecordPartitionArchivedOutput{ArchiveID: pa.ID.String(), Created: true}, nil
}
```

- [ ] **Step 4.6: stub_service_test.go**

```go
package billing_test

import (
	"context"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/billing"
)

func validInput() billing.RecordPartitionArchivedInput {
	return billing.RecordPartitionArchivedInput{
		Year: 2026, Month: 5, PartitionName: "usage_events_2026_05",
		LakeURI: "s3://lake/usage/2026/05/", RowCount: 1, BytesWritten: 100,
	}
}

func TestStubService_FirstCallCreates(t *testing.T) {
	s := billing.NewStubService()
	out, err := s.RecordPartitionArchived(context.Background(), validInput())
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !out.Created {
		t.Fatalf("expected Created=true, got false")
	}
	if out.ArchiveID == "" {
		t.Fatalf("expected non-empty ArchiveID")
	}
}

func TestStubService_RetryIsIdempotent(t *testing.T) {
	s := billing.NewStubService()
	in := validInput()
	first, _ := s.RecordPartitionArchived(context.Background(), in)
	second, err := s.RecordPartitionArchived(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if second.Created {
		t.Fatalf("expected Created=false on retry")
	}
	if second.ArchiveID != first.ArchiveID {
		t.Fatalf("expected same ArchiveID, got %s vs %s", second.ArchiveID, first.ArchiveID)
	}
}

func TestStubService_ValidationErrors(t *testing.T) {
	s := billing.NewStubService()
	cases := []struct {
		name  string
		mut   func(*billing.RecordPartitionArchivedInput)
		wantE error
	}{
		{"year-low", func(i *billing.RecordPartitionArchivedInput) { i.Year = 2025 }, billing.ErrInvalidYear},
		{"month-zero", func(i *billing.RecordPartitionArchivedInput) { i.Month = 0 }, billing.ErrInvalidMonth},
		{"empty-name", func(i *billing.RecordPartitionArchivedInput) { i.PartitionName = "" }, billing.ErrEmptyPartitionName},
		{"empty-uri", func(i *billing.RecordPartitionArchivedInput) { i.LakeURI = "" }, billing.ErrEmptyLakeURI},
		{"negative-rows", func(i *billing.RecordPartitionArchivedInput) { i.RowCount = -1 }, billing.ErrNegativeCount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mut(&in)
			_, err := s.RecordPartitionArchived(context.Background(), in)
			if err != tc.wantE {
				t.Fatalf("want %v got %v", tc.wantE, err)
			}
		})
	}
}
```

- [ ] **Step 4.7: Run unit tests**

```bash
go test -race ./plane/application/billing/... -count=1
```

Expected: PASS.

- [ ] **Step 4.8: Commit**

```bash
git add plane/application/billing/
git commit -m "$(cat <<'EOF'
feat(application): billing service interface + stub for #74

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Postgres service implementation + integration test

**Files:**
- Create: `plane/application/billing/postgres_service.go`
- Create: `plane/application/billing/postgres_service_test.go`

- [ ] **Step 5.1: postgres_service.go**

```go
package billing

import (
	"context"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// PostgresService implements Service against any store.MetadataStore.
// Constructor takes the interface so cmd/billing-service can inject a
// pgxpool-backed store and tests can inject the stub.
type PostgresService struct {
	store store.MetadataStore
	clock func() time.Time
}

// NewPostgresService wraps ms.
func NewPostgresService(ms store.MetadataStore) *PostgresService {
	return &PostgresService{store: ms, clock: func() time.Time { return time.Now().UTC() }}
}

func (s *PostgresService) RecordPartitionArchived(ctx context.Context, in RecordPartitionArchivedInput) (RecordPartitionArchivedOutput, error) {
	if err := validateInput(in); err != nil {
		return RecordPartitionArchivedOutput{}, err
	}

	candidate := PartitionArchive{
		ID:            uuid.New(),
		Year:          in.Year,
		Month:         in.Month,
		PartitionName: in.PartitionName,
		LakeURI:       in.LakeURI,
		RowCount:      in.RowCount,
		BytesWritten:  in.BytesWritten,
		ArchivedAt:    s.clock(),
	}

	var (
		archiveID uuid.UUID
		created   bool
	)
	err := s.store.Transact(ctx, func(tx store.Tx) error {
		id, ok, err := tx.Billing().InsertPartitionArchiveIfAbsent(ctx, candidate)
		if err != nil {
			return err
		}
		archiveID = id
		created = ok
		if !created {
			// Idempotent retry — outbox row was written on the first attempt.
			return nil
		}
		// Bind the chosen ID back into the payload before serialising.
		stored := candidate
		stored.ID = id
		return tx.WriteOutbox(ctx, store.DomainBilling, "partition_archive", id, EventTypePartitionArchived, newPartitionArchivedPayload(stored))
	})
	if err != nil {
		return RecordPartitionArchivedOutput{}, err
	}
	return RecordPartitionArchivedOutput{ArchiveID: archiveID.String(), Created: created}, nil
}
```

- [ ] **Step 5.2: postgres_service_test.go (integration)**

```go
//go:build integration

package billing_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/billing"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	storetest "github.com/gitscale-platform/gitscale/plane/data/store/postgres/postgrestest"
)

func TestPostgresService_RecordPartitionArchived_FirstAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	ms := storepg.New(pool)
	svc := billing.NewPostgresService(ms)

	in := billing.RecordPartitionArchivedInput{
		Year: 2026, Month: 5, PartitionName: "usage_events_2026_05",
		LakeURI: "s3://lake/billing/usage_events/2026/05/", RowCount: 12, BytesWritten: 4096,
	}

	first, err := svc.RecordPartitionArchived(ctx, in)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !first.Created {
		t.Fatalf("expected Created=true on first call")
	}

	// Source row exists.
	var sourceCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing.partition_archives WHERE partition_name=$1`, in.PartitionName).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Fatalf("expected 1 source row, got %d", sourceCount)
	}

	// Outbox row exists, with the right event_type and payload.
	var outboxCount int
	var rawPayload []byte
	if err := pool.QueryRow(ctx, `SELECT count(*), max(payload::text)::bytea FROM billing.billing_outbox WHERE event_type=$1`, billing.EventTypePartitionArchived).Scan(&outboxCount, &rawPayload); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("expected 1 outbox row, got %d", outboxCount)
	}
	var payload billing.PartitionArchivedPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.PartitionName != in.PartitionName || payload.RowCount != in.RowCount {
		t.Fatalf("payload mismatch: %+v", payload)
	}

	// Retry — idempotent, no second outbox row.
	second, err := svc.RecordPartitionArchived(ctx, in)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second.Created {
		t.Fatalf("expected Created=false on retry")
	}
	if second.ArchiveID != first.ArchiveID {
		t.Fatalf("ArchiveID changed across retry")
	}

	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing.billing_outbox WHERE event_type=$1`, billing.EventTypePartitionArchived).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("expected outbox count to stay 1 after retry, got %d", outboxCount)
	}
}
```

(Adjust import path of `postgrestest` to whatever exists; if no such package
exists, copy the testcontainer setup from `plane/application/identity/integration_test.go`.)

- [ ] **Step 5.3: Run integration test**

```bash
go test -tags integration -race -run TestPostgresService_RecordPartitionArchived ./plane/application/billing/... -count=1
```

Expected: PASS.

- [ ] **Step 5.4: Commit**

```bash
git add plane/application/billing/postgres_service.go \
        plane/application/billing/postgres_service_test.go
git commit -m "$(cat <<'EOF'
feat(application): billing PostgresService with outbox in same Tx (#74)

Idempotent on (year, month, partition_name); outbox row written only on
first insert (ADR-008).

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: gRPC server + unit test

**Files:**
- Create: `plane/application/billing/grpc_server.go`
- Create: `plane/application/billing/grpc_server_test.go`

- [ ] **Step 6.1: grpc_server.go**

```go
package billing

import (
	"context"
	"errors"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GRPCServer adapts the in-process Service to the generated BillingService surface.
type GRPCServer struct {
	billingv1.UnimplementedBillingServiceServer
	svc Service
}

// NewGRPCServer wraps svc.
func NewGRPCServer(svc Service) *GRPCServer { return &GRPCServer{svc: svc} }

func (s *GRPCServer) RecordPartitionArchived(ctx context.Context, req *billingv1.RecordPartitionArchivedRequest) (*billingv1.RecordPartitionArchivedResponse, error) {
	out, err := s.svc.RecordPartitionArchived(ctx, RecordPartitionArchivedInput{
		Year:          int(req.GetYear()),
		Month:         int(req.GetMonth()),
		PartitionName: req.GetPartitionName(),
		LakeURI:       req.GetLakeUri(),
		RowCount:      req.GetRowCount(),
		BytesWritten:  req.GetBytesWritten(),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &billingv1.RecordPartitionArchivedResponse{
		ArchiveId: out.ArchiveID,
		Created:   out.Created,
	}, nil
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, ErrInvalidYear),
		errors.Is(err, ErrInvalidMonth),
		errors.Is(err, ErrEmptyPartitionName),
		errors.Is(err, ErrEmptyLakeURI),
		errors.Is(err, ErrNegativeCount):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
```

- [ ] **Step 6.2: grpc_server_test.go**

```go
package billing_test

import (
	"context"
	"net"
	"testing"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"github.com/gitscale-platform/gitscale/plane/application/billing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func bufServer(t *testing.T, svc billing.Service) billingv1.BillingServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	billingv1.RegisterBillingServiceServer(srv, billing.NewGRPCServer(svc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return billingv1.NewBillingServiceClient(cc)
}

func TestGRPC_RecordPartitionArchived_HappyAndIdempotent(t *testing.T) {
	c := bufServer(t, billing.NewStubService())
	req := &billingv1.RecordPartitionArchivedRequest{
		Year: 2026, Month: 5, PartitionName: "usage_events_2026_05",
		LakeUri: "s3://lake/", RowCount: 1, BytesWritten: 100,
	}
	first, err := c.RecordPartitionArchived(context.Background(), req)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if !first.GetCreated() {
		t.Fatal("expected created on first")
	}
	second, err := c.RecordPartitionArchived(context.Background(), req)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.GetCreated() {
		t.Fatal("expected created=false on retry")
	}
	if second.GetArchiveId() != first.GetArchiveId() {
		t.Fatal("archive_id changed across retry")
	}
}

func TestGRPC_ValidationErrors_MapToInvalidArgument(t *testing.T) {
	c := bufServer(t, billing.NewStubService())
	cases := []struct {
		name string
		mut  func(*billingv1.RecordPartitionArchivedRequest)
	}{
		{"year-low", func(r *billingv1.RecordPartitionArchivedRequest) { r.Year = 2025 }},
		{"month-zero", func(r *billingv1.RecordPartitionArchivedRequest) { r.Month = 0 }},
		{"empty-name", func(r *billingv1.RecordPartitionArchivedRequest) { r.PartitionName = "" }},
		{"empty-uri", func(r *billingv1.RecordPartitionArchivedRequest) { r.LakeUri = "" }},
		{"negative-rows", func(r *billingv1.RecordPartitionArchivedRequest) { r.RowCount = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &billingv1.RecordPartitionArchivedRequest{
				Year: 2026, Month: 5, PartitionName: "usage_events_2026_05",
				LakeUri: "s3://lake/", RowCount: 1, BytesWritten: 100,
			}
			tc.mut(req)
			_, err := c.RecordPartitionArchived(context.Background(), req)
			st, _ := status.FromError(err)
			if st.Code() != codes.InvalidArgument {
				t.Fatalf("want InvalidArgument, got %v (%v)", st.Code(), err)
			}
		})
	}
}
```

- [ ] **Step 6.3: Run tests**

```bash
go test -race ./plane/application/billing/... -count=1
```

Expected: PASS.

- [ ] **Step 6.4: Commit**

```bash
git add plane/application/billing/grpc_server.go \
        plane/application/billing/grpc_server_test.go
git commit -m "$(cat <<'EOF'
feat(application): billing gRPC server adapter (#74)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: cmd/billing-service binary + boot smoke test

**Files:**
- Create: `cmd/billing-service/main.go`
- Create: `cmd/billing-service/integration_test.go`

- [ ] **Step 7.1: main.go (mirror cmd/identity-service)**

Read `cmd/identity-service/main.go` and mirror it. Differences:

- Env var prefix: `BILLING_SERVICE_*`.
- Default `Addr`: `:8087` (identity is `:8085`; pick a free port; cross-check
  any docker-compose or k8s manifest).
- `NewGRPCServer(billing.NewPostgresService(...))` instead of identity.

Skeleton (fill in helper bodies — getenv, getenvBool — by copying from
identity main):

```go
// billing-service is the application-plane gRPC binary for the billing
// domain (ADR-019). State-mutating RPCs flow through this process exclusively;
// the workflow plane reaches it via plane/workflow/appclient.
//
// mTLS via SPIRE/SPIFFE (ADR-010) is the production posture. Until SPIRE
// rolls, the binary boots with insecure transport credentials when
// BILLING_SERVICE_INSECURE=true.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"github.com/gitscale-platform/gitscale/plane/application/billing"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
)

type config struct {
	Addr        string
	PostgresDSN string
	Insecure    bool
}

func loadConfig() (config, error) {
	cfg := config{
		Addr:     getenv("BILLING_SERVICE_ADDR", ":8087"),
		Insecure: getenvBool("BILLING_SERVICE_INSECURE", false),
	}
	cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")
	if cfg.PostgresDSN == "" {
		return cfg, errors.New("POSTGRES_DSN is required")
	}
	if !cfg.Insecure {
		return cfg, errors.New("only BILLING_SERVICE_INSECURE=true is supported until SPIRE/SPIFFE wiring lands (ADR-010)")
	}
	return cfg, nil
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pool.Close()

	ms := storepg.New(pool)
	svc := billing.NewPostgresService(ms)
	grpcSrv := grpc.NewServer()
	billingv1.RegisterBillingServiceServer(grpcSrv, billing.NewGRPCServer(svc))

	lis, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		grpcSrv.GracefulStop()
	}()

	log.Printf("billing-service listening on %s", cfg.Addr)
	if err := grpcSrv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getenvBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v == "true" || v == "1"
}
```

- [ ] **Step 7.2: integration_test.go**

Mirror the spirit of `cmd/identity-service/integration_test.go` — boot the
binary against the testcontainer Postgres, dial the gRPC port, call
`RecordPartitionArchived`, assert success.

- [ ] **Step 7.3: Build + test**

```bash
go build ./cmd/billing-service
go test -tags integration -race ./cmd/billing-service/... -count=1
```

Expected: binary builds; integration test passes.

- [ ] **Step 7.4: Commit**

```bash
git add cmd/billing-service/
git commit -m "$(cat <<'EOF'
feat(cmd): billing-service binary for #74

Mirrors cmd/identity-service: pgxpool + gRPC listener; SIGTERM-clean
shutdown; SPIFFE wiring blocked on ADR-010.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: appclient gRPC client adapter (replaces stub at the wiring layer in #76)

**Files:**
- Create: `plane/workflow/appclient/billing_grpc.go`
- Update if needed: `plane/workflow/appclient/billing_grpc_test.go`

The existing `BillingClient` interface and `StubBillingClient` in
`plane/workflow/appclient/billing.go` are NOT modified.

- [ ] **Step 8.1: billing_grpc.go**

```go
package appclient

import (
	"context"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"google.golang.org/grpc"
)

// grpcBillingClient implements BillingClient against a generated
// BillingServiceClient. Per ADR-019, RecordPartitionArchived is a single
// unary RPC into cmd/billing-service which performs the source-row +
// outbox-row write in one Tx (ADR-008). This adapter holds no state; the
// caller owns the *grpc.ClientConn lifecycle.
type grpcBillingClient struct {
	c billingv1.BillingServiceClient
}

// NewGRPCBillingClient returns a BillingClient backed by an existing gRPC
// client connection.
func NewGRPCBillingClient(cc *grpc.ClientConn) BillingClient {
	return &grpcBillingClient{c: billingv1.NewBillingServiceClient(cc)}
}

func (g *grpcBillingClient) RecordPartitionArchived(ctx context.Context, in PartitionArchivedInput) error {
	_, err := g.c.RecordPartitionArchived(ctx, &billingv1.RecordPartitionArchivedRequest{
		Year:          int32(in.Year),
		Month:         int32(in.Month),
		PartitionName: in.PartitionName,
		LakeUri:       in.LakeURI,
		RowCount:      in.RowCount,
		BytesWritten:  in.BytesWritten,
	})
	return err
}
```

- [ ] **Step 8.2: Compile**

```bash
go build ./plane/workflow/appclient/...
go vet ./plane/workflow/appclient/...
```

Expected: success.

- [ ] **Step 8.3: Commit**

```bash
git add plane/workflow/appclient/billing_grpc.go
git commit -m "$(cat <<'EOF'
feat(workflow): appclient gRPC adapter for BillingService (#74)

Workflow-worker wiring lands in #76; the stub continues to back unit tests.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: End-to-end integration test through the gRPC adapter

**File:**
- Create: `plane/application/billing/integration_test.go`

- [ ] **Step 9.1: Write the test (build tag `integration`)**

```go
//go:build integration

package billing_test

import (
	"context"
	"net"
	"testing"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"github.com/gitscale-platform/gitscale/plane/application/billing"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	storetest "github.com/gitscale-platform/gitscale/plane/data/store/postgres/postgrestest"
	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestE2E_AppclientToOutbox(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	ms := storepg.New(pool)
	svc := billing.NewPostgresService(ms)

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	billingv1.RegisterBillingServiceServer(srv, billing.NewGRPCServer(svc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	client := appclient.NewGRPCBillingClient(cc)

	in := appclient.PartitionArchivedInput{
		Year: 2026, Month: 5, PartitionName: "usage_events_2026_05",
		LakeURI: "s3://lake/billing/usage_events/2026/05/",
		RowCount: 42, BytesWritten: 8192,
	}
	if err := client.RecordPartitionArchived(ctx, in); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := client.RecordPartitionArchived(ctx, in); err != nil {
		t.Fatalf("retry: %v", err)
	}

	var sourceCount, outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing.partition_archives WHERE partition_name=$1`, in.PartitionName).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing.billing_outbox WHERE event_type=$1`, billing.EventTypePartitionArchived).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 || outboxCount != 1 {
		t.Fatalf("expected 1 source / 1 outbox row, got %d / %d", sourceCount, outboxCount)
	}
}
```

- [ ] **Step 9.2: Run**

```bash
go test -tags integration -race -run TestE2E_AppclientToOutbox ./plane/application/billing/... -count=1
```

Expected: PASS.

- [ ] **Step 9.3: Commit**

```bash
git add plane/application/billing/integration_test.go
git commit -m "$(cat <<'EOF'
test(application): e2e billing path appclient → grpc → outbox (#74)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Final gates + open PR

- [ ] **Step 10.1: Full test sweep**

```bash
go build ./...
go vet ./...
golangci-lint run
go test -race ./... -count=1
go test -tags integration -race ./... -count=1
```

Expected: all green. If `golangci-lint` is not installed, install per repo's
docs/runbook entry; do not skip the lint step.

- [ ] **Step 10.2: Run mandatory skills (each is a separate Skill invocation)**

Per supervisor §6 for an application-plane PR:

- `gitscale-go-conventions`
- `gitscale-outbox-check`
- `gitscale-adr-guard`
- `gitscale-plane-boundary`

Resolve every actionable finding before proceeding.

- [ ] **Step 10.3: Self-review battery (parallel subagent dispatch)**

Dispatch all in one message:

- `pr-review-toolkit:code-reviewer`
- `pr-review-toolkit:silent-failure-hunter`
- `pr-review-toolkit:type-design-analyzer` (new public types: `Service`, `PostgresService`, `GRPCServer`, `RecordPartitionArchivedInput`, `RecordPartitionArchivedOutput`, `PartitionArchivedPayload`, `BillingReader`, `BillingWriter`, `PartitionArchive`)
- `pr-review-toolkit:pr-test-analyzer`
- `adr-historian` — confirm ADR-008 + ADR-019 conformance

If `adr-historian` returns `contradicts`, stop and file a `type/adr` issue.

- [ ] **Step 10.4: Push**

```bash
git push -u origin feat/application-billing-grpc-service
```

- [ ] **Step 10.5: Open PR**

```bash
gh pr create --title "[Application] BillingClient gRPC impl + billing app-plane service for RecordPartitionArchived" --body "$(cat <<'EOF'
## Summary

- Adds `plane/application/billing/` mirroring `plane/application/identity/`:
  service interface, postgres impl with same-Tx outbox write (ADR-008),
  stub, gRPC adapter.
- New `cmd/billing-service` binary and `plane/workflow/appclient/billing_grpc.go`
  (worker wiring lives in #76).
- New `billing.partition_archives` table + UNIQUE(year,month,partition_name)
  anchors idempotency under Temporal activity retry.

## ADR-impact

conforming. ADR-008 (outbox in same Tx) and ADR-019 (workflow→app-plane gRPC
boundary) are honoured exactly.

## Test plan

- [x] `go test -race ./plane/application/billing/...` (unit + grpc table-driven)
- [x] `go test -tags integration -race ./plane/data/store/billing/...`
- [x] `go test -tags integration -race ./plane/application/billing/...` (PG roundtrip + outbox-row assertion)
- [x] `go test -tags integration -race ./cmd/billing-service/...` (binary boots, gRPC reachable)
- [x] `go build ./... && go vet ./... && golangci-lint run`

Spec: docs/superpowers/specs/2026-05-08-issue-74-billing-grpc-service-design.md
Plan: docs/superpowers/plans/2026-05-08-issue-74-billing-grpc-service-plan.md

<details><summary>Self-review</summary>

- code-reviewer: <result>
- silent-failure-hunter: <result>
- type-design-analyzer: <result>
- pr-test-analyzer: <result>
- adr-historian: <result>

</details>

Closes #74.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 10.6: Watch CI**

```bash
gh pr checks <number> --watch
```

Expected: `go.yml`, `lint-events`, `lint-md` all green. Fix any red checks
with new commits (never amend, never `--no-verify`).

---

## Self-review (plan author)

**Spec coverage:**
- Proto — Task 3.
- Migration — Task 1.
- `plane/application/billing/{service,postgres_service,grpc_server,stub,events,models,doc}.go` — Tasks 4 + 5 + 6.
- `appclient/billing_grpc.go` — Task 8.
- `cmd/billing-service` — Task 7.
- Integration test asserting outbox row — Tasks 5 + 9.
- Idempotency contract — Tasks 2 + 5 (postgres + service layer).

**Placeholder scan:** None.

**Type consistency:**
- `RecordPartitionArchivedInput/Output` — used across service, gRPC server, tests.
- `PartitionArchive` — alias of `store.PartitionArchive`; consistent.
- `EventTypePartitionArchived` — referenced in service + integration test + payload constructor.
- `BillingReader/Writer` — defined in Task 2, consumed in Task 5.

No drift detected.
