# Spec — Issue #74 BillingClient gRPC + billing app-plane service

Date: 2026-05-08
Issue: https://github.com/gitscale-platform/gitscale/issues/74
Plane: application
Priority: p1 (Wave 0)
ADR-impact: conforming (ADR-008 outbox pattern, ADR-019 workflow→app-plane boundary)

## Problem

`appclient.BillingClient` is a stub. `EmitArchiveEventActivity` in the
partition-archive workflow calls `BillingClient.RecordPartitionArchived`, which
silently succeeds via `StubBillingClient` and never writes a
`billing.partition_archived` outbox row. Production runs of the archive
workflow drop the billing event on the floor.

## Goals

1. Implement a real billing app-plane service mirroring `plane/application/identity/`.
2. Persist a source row for every archived partition in a new
   `billing.partition_archives` table and emit a `billing.partition_archived`
   row to `billing.billing_outbox` in the same transaction (ADR-008).
3. Provide a gRPC client in `plane/workflow/appclient/` that the workflow plane
   uses to talk to the service across the plane boundary (ADR-019).
4. Deliver an integration test that asserts the outbox row appears after a
   successful gRPC call.

## Non-goals

- Wiring the real client into `cmd/workflow-worker` (issue #76, Wave 1).
- End-to-end Temporal workflow integration test (issue #78, Wave 2).
- Glue Data Catalog registration (issue #77, Wave 2).
- Any other billing surface (only `RecordPartitionArchived` is in scope).

## Architecture

### Proto surface

New package: `internal/proto/gitscale/billing/v1/billing.proto`.

```
service BillingService {
  rpc RecordPartitionArchived(RecordPartitionArchivedRequest)
      returns (RecordPartitionArchivedResponse);
}

message RecordPartitionArchivedRequest {
  int32  year           = 1;  // 2026..
  int32  month          = 2;  // 1..12
  string partition_name = 3;  // e.g. "usage_events_2026_05"
  string lake_uri       = 4;  // canonical s3:// URI returned by ObjectStore.Upload
  int64  row_count      = 5;
  int64  bytes_written  = 6;
}

message RecordPartitionArchivedResponse {
  string archive_id = 1;  // UUID of partition_archives row (new or existing)
  bool   created    = 2;  // false on idempotent retry
}
```

### Schema

New migration: `plane/data/migrations/006_billing_partition_archives.sql`.

```sql
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
```

No FK to `billing.usage_events` — the partition is a `pg_class` object, not a
data row, and the archive record outlives the partition (post-DROP cleanup).

### Service package — `plane/application/billing/`

Files mirror identity exactly:

| File | Responsibility |
|---|---|
| `doc.go` | Package doc, ADR refs |
| `models.go` | `PartitionArchive` struct, `RecordPartitionArchivedInput/Output` |
| `events.go` | `EventTypePartitionArchived = "billing.partition_archived"`, payload struct |
| `service.go` | `Service` interface |
| `postgres_service.go` | `PostgresService` implementing `Service` against `*pgxpool.Pool` |
| `stub_service.go` | In-memory `StubService` for grpc_server_test |
| `grpc_server.go` | `GRPCServer` adapting `Service` to generated gRPC surface |
| `integration_test.go` | testcontainers PG end-to-end test |
| `postgres_service_test.go` | testcontainers PG service-level test |
| `grpc_server_test.go` | gRPC error-mapping table-driven test (uses StubService) |
| `stub_service_test.go` | StubService unit test |

### Idempotency contract

`PostgresService.RecordPartitionArchived` opens a single Tx and runs:

```sql
INSERT INTO billing.partition_archives
       (year, month, partition_name, lake_uri, row_count, bytes_written)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (year, month, partition_name) DO NOTHING
RETURNING id;
```

- 1 row returned → first write. Insert outbox row in same Tx, return `(id, created=true)`.
- 0 rows returned → idempotent retry. Run `SELECT id FROM billing.partition_archives WHERE (year,month,partition_name)=…`, return `(id, created=false)`. **No second outbox row.**

This guarantees exactly-one outbox event per archived partition, even under
unbounded Temporal activity retry.

### gRPC client — `plane/workflow/appclient/billing_grpc.go`

Mirrors `identity_grpc.go`:

- Constructor `NewGRPCBillingClient(conn *grpc.ClientConn) BillingClient`.
- Implements existing `BillingClient` interface (no breaking change to workflow
  code).
- Translates `PartitionArchivedInput` → `RecordPartitionArchivedRequest`.
- Returns `nil` on success regardless of `created` flag (workflow does not
  care; both outcomes are success from the activity's perspective).

Existing `StubBillingClient` retained — workflow unit tests in
`plane/workflow/archive/` rely on it.

### Binary — `cmd/billing-service/main.go`

Mirrors `cmd/identity-service` exactly:

- Reads `DATABASE_URL` env var, opens `pgxpool`.
- Constructs `PostgresService`.
- Wraps in `GRPCServer`, starts gRPC listener on `:50053` (identity uses 50051;
  outbox-consumer uses 50052).
- Graceful shutdown on SIGTERM.

### Error mapping (gRPC layer)

| Service error | gRPC code |
|---|---|
| Validation (year/month range, empty partition_name, negative counts) | `InvalidArgument` |
| `pgx.ErrConnLost` / pool timeout | `Unavailable` |
| Anything else | `Internal` |

Conflict path is **not** an error — returns OK with `created=false`.

## Test plan

| Layer | Test |
|---|---|
| Unit (StubService) | New call appends; idempotent retry returns same id, no duplicate event |
| Service (testcontainer PG) | First call inserts source + outbox; second call no-op both, returns same id; concurrent goroutines on same key — exactly one source row, exactly one outbox row |
| gRPC (StubService + bufconn) | Validation errors mapped correctly; conflict returns OK; unknown error returns Internal |
| Integration (testcontainer PG + bufconn) | Full path: gRPC client → server → PG. Assert `billing.partition_archives` row + `billing.billing_outbox` row both visible; outbox payload JSON matches `EventTypePartitionArchived` schema |

All testcontainer tests gated by `//go:build integration` to keep the default
`go test ./...` fast.

## Acceptance checklist (from issue body)

- [ ] Proto generated and committed under `internal/proto/gitscale/billing/v1/`
- [ ] `plane/application/billing/{service.go, postgres_service.go, grpc_server.go}` implemented
- [ ] `plane/workflow/appclient/billing_grpc.go` implemented (client wired into `cmd/workflow-worker` is **deferred to #76**)
- [ ] Integration test asserts `billing_outbox` row appears after `RecordPartitionArchived` call
- [ ] PR description references ADR-008 + ADR-019

## Open questions

None — all design decisions resolved.

## References

- ADR-008 (outbox pattern) — `docs/architecture.md §8`
- ADR-019 (workflow→app-plane plane boundary) — `docs/architecture.md §8`
- Pattern reference: `plane/application/identity/` (issue #15, PR ~#56)
- Stub it replaces: `plane/workflow/appclient/billing.go::StubBillingClient`
- Workflow consumer: `plane/workflow/archive/` (issue #69, PR #73)
