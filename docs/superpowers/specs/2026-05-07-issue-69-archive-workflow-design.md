# Design: #69 — Billing partition archive workflow

**Date:** 2026-05-07
**Issue:** #69 `[Workflow] usage_events partition archive workflow per ADR-018`
**ADR:** ADR-018 (analytics-lake archival), ADR-019 (workflow→app-plane boundary)
**Spec:** `2026-05-06-issue-34-billing-archival-tier-spike.md` (archival decisions)
**Pre-merge of:** PR #68 (rollover), ADR-018 merged (PR #41)
**Branch:** `feat/workflow-billing-partition-archive`

## 1. Scope

This PR is `plane/workflow` only. Out of scope, each tracked as a follow-up issue:

- `appclient.BillingClient` gRPC implementation + billing app-plane service (`RecordPartitionArchived` endpoint + `billing_outbox` write) — follow-up `plane/application` issue
- Glue Data Catalog registration activity — follow-up `plane/data` issue (Terraform + IAM + Glue API)
- `RestorePartition` workflow — acknowledged by ADR-018, deferred
- Vault SDK wiring for `KeyProvider` — follow-up infrastructure issue; this PR ships a deterministic stub

## 2. Activity sequencing — chosen approach

```
DetachPartition → ExportToObjectStore → EmitOutbox → DropPartition
```

Detach-first gives a stable, sealed snapshot for export. No new rows can enter the partition during the Parquet stream. Compensation on `DropPartition` failure: data exists in both PG (detached) and object store — no loss, page + stop. Manual runbook: verify object store integrity then `DROP TABLE` or re-run workflow.

Alternatives considered:

- **Export-first, detach-last** — no dark window but partition is 18+ months old with no live writers; snapshot stability benefit is theoretical.
- **Detach → Export → Drop → Emit** — places the irreversible step (Drop) before the outbox emit; a failed Emit after Drop leaves no notification recovery path.

## 3. New files

### `plane/workflow/billing/`

| File | Purpose |
|---|---|
| `archive_workflow.go` | `PartitionArchiveWorkflow` — orchestrates 4 activities |
| `detach_activity.go` | `DetachPartitionActivity` — `DETACH PARTITION CONCURRENTLY` via `billing.Archiver` |
| `export_activity.go` | `ExportActivity` — stream → Parquet+zstd → AES-256-GCM → object store multipart |
| `emit_activity.go` | `EmitArchiveEventActivity` — calls `appclient.BillingClient.RecordPartitionArchived` |
| `drop_activity.go` | `DropPartitionActivity` — `DROP TABLE billing.usage_events_YYYY_MM` |
| `objectstore.go` | `ObjectStore` interface + `stubObjectStore` |
| `objectstore_s3.go` | `s3ObjectStore` — S3-compatible impl (AWS, minio, R2, GCS interop) |
| `archive_schedules.go` | `EnsureArchiveSchedule` — monthly cron, `gswf.EnsureSchedule` |
| `archive_workflow_test.go` | Temporal test suite: happy path + mid-export crash resumption |

### `plane/workflow/appclient/`

| File | Purpose |
|---|---|
| `billing.go` | `BillingClient` interface + `NewStubBillingClient` |

### `plane/data/store/billing/`

| File | Purpose |
|---|---|
| `archiver.go` | `Archiver` interface + `RowCursor` + `UsageEventRow` |
| `archiver_postgres.go` | Postgres impl + `StubArchiver` |

### Extended files

| File | Change |
|---|---|
| `bundle.go` | Add archive workflow + 4 activities + `BillingClient` + `ObjectStore` + `KeyProvider` deps |
| `schedules.go` | Add `EnsureArchiveSchedule` call |
| `docker-compose.yml` | Add `minio` service (port 9000 S3 API, 9001 console) |

### New dependencies (`go.mod`)

- `github.com/parquet-go/parquet-go`
- `github.com/aws/aws-sdk-go-v2/service/s3`

## 4. Interfaces

### `billing.Archiver` (data store layer)

```go
type Archiver interface {
    // DetachUsageEventsPartition issues ALTER TABLE … DETACH PARTITION … CONCURRENTLY.
    // Idempotent: no-op if already detached.
    DetachUsageEventsPartition(ctx context.Context, year, month int) error

    // DropUsageEventsPartition drops the detached billing.usage_events_YYYY_MM table.
    // Must only be called after object store upload + outbox emit succeed.
    DropUsageEventsPartition(ctx context.Context, year, month int) error

    // ScanPartitionRows returns a cursor over all rows in the detached partition.
    // Used by ExportActivity to stream without loading full partition into memory.
    ScanPartitionRows(ctx context.Context, year, month int) (RowCursor, error)
}

type RowCursor interface {
    Next(ctx context.Context) bool
    Row() UsageEventRow
    Err() error
    Close() error
}

// UsageEventRow mirrors the billing.usage_events column set from 005_billing.sql.
// Fields must match exactly — this struct drives the Parquet schema via struct tags.

```

### `appclient.BillingClient` (workflow → app-plane, ADR-019)

```go
type BillingClient interface {
    // RecordPartitionArchived writes billing.partition_archived to the billing
    // outbox via the billing app-plane service (gRPC impl is a follow-up issue).
    RecordPartitionArchived(ctx context.Context, in PartitionArchivedInput) error
}

type PartitionArchivedInput struct {
    Year          int
    Month         int
    PartitionName string
    LakeURI       string  // canonical URI returned by ObjectStore.Upload
    RowCount      int64
    BytesWritten  int64
}
```

### `ObjectStore` (provider-agnostic)

```go
type ObjectStore interface {
    // Upload streams r to key. Implementation handles multipart internally for
    // large objects. Returns the canonical URI (e.g. s3://bucket/key) for the
    // outbox event and manifest.
    Upload(ctx context.Context, key string, r io.Reader, sizeHint int64) (uri string, err error)

    // PutBytes writes a small object (manifest JSON, checksum file).
    PutBytes(ctx context.Context, key string, data []byte) error
}
```

`s3ObjectStore` uses AWS SDK v2 with a configurable endpoint — same code path covers production S3, minio (local dev), Cloudflare R2, and GCS S3-interop mode. No provider coupling in activity code.

### `KeyProvider` (encryption boundary)

```go
type KeyProvider interface {
    // GetDEK returns a 32-byte AES-256 key for the given (year, month).
    // Production: HKDF(platform_billing_master, "YYYY-MM") via Vault transit.
    // Stub: deterministic fixed key derived from year+month.
    GetDEK(ctx context.Context, year, month int) ([]byte, error)
}
```

## 5. Workflow contract

```go
type ArchiveInput struct {
    RunTime time.Time // schedule's scheduled-time — same determinism pattern as rollover
    Year    int
    Month   int
}

type ArchiveResult struct {
    PartitionName string
    LakeURI       string
    RowCount      int64
    BytesWritten  int64
}

func PartitionArchiveWorkflow(ctx workflow.Context, in ArchiveInput) (ArchiveResult, error)
```

**Target partition:** month = `RunTime - 18 months`. Pure function of `RunTime`; deterministic across replays.

**Activity timeouts:**

| Activity | StartToCloseTimeout | Heartbeat |
|---|---|---|
| DetachPartition | 5 min | — |
| Export | 4 h | every 10k rows |
| EmitOutbox | 1 min | — |
| DropPartition | 5 min | — |

All use `gswf.DefaultRetryPolicy()`.

**Idempotency:** Workflow ID = `"billing-partition-archive-YYYY-MM"`. Temporal rejects duplicates while running (`REJECT_DUPLICATE`); allows retry after crash (`ALLOW_DUPLICATE_FAILED_ONLY`).

## 6. Schedule

```go
const ArchiveScheduleID    = "billing-partition-archive"
const ArchiveCronExpression = "0 14 24 * *"  // 14:00 UTC, 2h after rollover
```

`EnsureArchiveSchedule` wraps `gswf.EnsureSchedule`. Schedule passes its own scheduled-time as `RunTime` — same pattern as rollover.

## 7. Data flow + object store layout

```
billing.usage_events_YYYY_MM (detached PG table)
  → ScanPartitionRows cursor (10k row batches)
  → parquet-go writer (zstd, schema from UsageEventRow struct tags)
  → AES-256-GCM encryption (32-byte DEK from KeyProvider)
  → ObjectStore.Upload (multipart, 64MB parts)
  → on commit:
      ObjectStore.PutBytes(.manifest.json)
      ObjectStore.PutBytes(.checksum.sha256)   ← SHA-256 of encrypted bytes, computed during stream
```

**Key layout (Hive-style for Athena/Trino partition pruning):**

```
billing/usage_events/year=YYYY/month=MM/
  usage_events_YYYY_MM.parquet
  usage_events_YYYY_MM.manifest.json
  usage_events_YYYY_MM.checksum.sha256
```

**Manifest JSON:**

```json
{
  "schema_version": 1,
  "source_partition": "billing.usage_events_2026_05",
  "row_count": 1000000000,
  "bytes_written": 62914560000,
  "kek_hint": "platform-billing-v1",
  "archive_ts": "2027-11-24T14:03:22Z",
  "checksum_alg": "sha256"
}
```

`kek_hint` records the key version for future DEK destruction (post-7y retention) and the deferred `RestorePartition` workflow.

## 8. Error handling

| Failure | Behaviour |
|---|---|
| DetachPartition fails | Retry — DETACH on already-detached partition is a no-op |
| Export fails mid-stream | Activity returns error; workflow retries from scratch. `AbortMultipartUpload` issued at start of each retry attempt before re-uploading to same key |
| EmitOutbox fails | Retry — stub returns nil; real gRPC client retries at transport |
| DropPartition fails after emit | Workflow surfaces error; data exists in both PG (detached) and object store. No data loss. Page + stop — runbook: verify object store integrity, then `DROP TABLE` manually or re-run workflow |
| Heartbeat timeout during export | Activity context cancelled; workflow retries Export from scratch |

## 9. Local dev setup

Add to `docker-compose.yml`:

```yaml
minio:
  image: minio/minio:latest
  command: server /data --console-address ":9001"
  environment:
    MINIO_ROOT_USER: gitscale
    MINIO_ROOT_PASSWORD: gitscale
  ports:
    - "9000:9000"
    - "9001:9001"
  healthcheck:
    test: ["CMD", "curl", "-f", "http://localhost:9000/minio/health/live"]
    interval: 5s
    timeout: 5s
    retries: 5
```

Worker env vars for local dev:

```
S3_ENDPOINT=http://localhost:9000
S3_BUCKET=gitscale-analytics-local
AWS_ACCESS_KEY_ID=gitscale
AWS_SECRET_ACCESS_KEY=gitscale
```

`s3ObjectStore` accepts a configurable endpoint — same binary, different config per environment.

## 10. Testing

**Unit (Temporal test suite, `archive_workflow_test.go`):**
- Happy path: all 4 activities succeed; `ArchiveResult` has correct `LakeURI`, `RowCount`, `BytesWritten`
- Mid-export crash resumption: `ExportActivity` mock fails on first call, succeeds on second; workflow completes
- Drop failure after emit: workflow returns error; `EmitActivity` was called, `DropActivity` was not retried beyond policy

**Activity unit tests (no Temporal harness):**
- Each activity tested directly with interface stubs
- `ExportActivity`: stub cursor + stub object store; asserts manifest JSON shape, checksum written, correct key path
- `DetachPartitionActivity`: asserts `Archiver.DetachUsageEventsPartition` called with correct (year, month)

**Integration (`-tags integration`, testcontainers):**
- PG testcontainer: real `DETACH PARTITION CONCURRENTLY` + `DROP TABLE`
- minio testcontainer: real multipart upload; asserts Parquet file present, manifest + checksum present, row count matches
- Recording `BillingClient` stub: asserts `RecordPartitionArchived` called with correct `LakeURI` and `RowCount`
- Crash-mid-archive: kill export mid-stream, re-run workflow; asserts idempotent completion

**No localstack-Glue** — deferred with follow-up issue filed in PR description.

## 11. Follow-up issues to file in PR description

1. `appclient.BillingClient` gRPC impl + billing app-plane service (`RecordPartitionArchived`)
2. Glue Data Catalog registration activity
3. `KeyProvider` Vault HKDF wiring
4. `RestorePartition` workflow
5. Per-month DEK destruction workflow (post-7y retention enforcement)

## 12. Cross-references

- ADR-018 — storage target, format, retention, encryption decisions
- ADR-019 — outbox emit routed via `appclient.BillingClient`, not direct from workflow
- ADR-008 — outbox pattern (honoured by billing app-plane service, not this PR)
- PR #68 — rollover (pre-merge); lays partition machinery this workflow operates on
- `2026-05-06-issue-34-billing-archival-tier-spike.md` — archival spike + ADR-018 text
- `2026-05-06-plan-issue-18-archive.md` — original plan (this spec supersedes it where they differ)
