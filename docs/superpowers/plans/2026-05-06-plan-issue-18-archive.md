# Plan: #18-archive — Billing partition detach + Parquet archive

**Date:** 2026-05-06
**Issue:** #18 (archive arm)
**Spec:** `2026-05-06-issue-34-billing-archival-tier-spike.md` (full archival decisions)
**Branch:** `feat/workflow-billing-partition-archive`
**Pre-merge of:** #33, #18-rollover, #34 (ADR-018), #15-revocation (for `appclient.IdentityClient` pattern — provides reference for `appclient.BillingClient`)
**Blocks:** none

## Pre-flight

- Confirm ADR-018 merged.
- Confirm `cmd/workflow-worker` running #18-rollover successfully for at least one cycle.
- File `appclient.BillingClient` interface (mirrors identity pattern) — gates this PR if billing app-plane service does not yet exist; if absent, this PR is blocked on a "billing app-plane service" issue (out of scope here).
- `git fetch && git checkout main && git pull`
- `git checkout -b feat/workflow-billing-partition-archive`

## Step sequence

### Step 1 — Provision analytics-lake S3 bucket

Out-of-band ops task (Terraform module — not in this PR but documented as a hard prereq):

- Bucket `gitscale-analytics-${env}` with lifecycle policy per ADR-018.
- IAM policy: workflow worker SPIFFE identity has write; analyst role has read; nobody else.
- Glue Data Catalog database `gitscale_analytics` (or Hive metastore).
- Vault transit engine `billing-archive` with platform KEK.

PR description must reference the Terraform PR or be gated on it.

### Step 2 — Detach activity

File: `plane/workflow/billing/detach_activity.go`

```go
type DetachPartitionActivity struct {
    store store.MetadataStore
}

func (a *DetachPartitionActivity) Execute(ctx context.Context, in DetachInput) error {
    // ALTER TABLE billing.usage_events DETACH PARTITION billing.usage_events_YYYY_MM CONCURRENTLY
    return a.store.Transact(ctx, func(tx store.Tx) error {
        return tx.Exec(ctx, fmt.Sprintf(
            "ALTER TABLE billing.usage_events DETACH PARTITION billing.usage_events_%04d_%02d CONCURRENTLY",
            in.Year, in.Month,
        ))
    })
}
```

### Step 3 — Stream-to-Parquet activity

File: `plane/workflow/billing/parquet_export_activity.go`

```go
import "github.com/parquet-go/parquet-go"
import vault "github.com/hashicorp/vault/api"
import s3 "github.com/aws/aws-sdk-go-v2/service/s3"

type ParquetExportActivity struct {
    store     store.MetadataStore
    s3Client  *s3.Client
    vault     *vault.Client
    kekName   string
    bucket    string
}

func (a *ParquetExportActivity) Execute(ctx context.Context, in ExportInput) (ExportOutput, error) {
    // 1. Derive DEK from Vault: HKDF(platform_master, year-month)
    // 2. Open detached partition for read.
    // 3. Stream rows in batches of 10k → Parquet writer (zstd) → encrypt with DEK → S3 multipart upload.
    // 4. Compute SHA-256 hash during stream.
    // 5. Upload .checksum.sha256 + .manifest.json siblings.
    // 6. Return row count + bytes written for outbox event.
}
```

Path: `s3://gitscale-analytics-${env}/billing/usage_events/year=YYYY/month=MM/usage_events_YYYY_MM.parquet`.

### Step 4 — Glue catalog register activity

File: `plane/workflow/billing/glue_register_activity.go`

Calls Glue API to add the partition pointer to the `gitscale_analytics.usage_events` table. Idempotent.

### Step 5 — Drop detached partition activity

File: `plane/workflow/billing/drop_detached_activity.go`

```go
DROP TABLE billing.usage_events_YYYY_MM
```

Only runs if upload + manifest + catalog register all succeeded.

### Step 6 — Emit `billing.partition_archived` event

Per ADR-019: route via `appclient.BillingClient.RecordPartitionArchived(...)`. The billing app-plane service writes the outbox row.

### Step 7 — Archive workflow

File: `plane/workflow/billing/archive_workflow.go`

```go
func PartitionArchiveWorkflow(ctx workflow.Context, in ArchiveInput) error {
    // Activity 1: Detach
    // Activity 2: Stream to Parquet (long-running; heartbeat)
    // Activity 3: Register in Glue
    // Activity 4: Emit outbox via appclient.BillingClient
    // Activity 5: Drop detached PG partition
    // Compensation: if step 5 fails after steps 1-4 succeeded → log + page; data exists in both places, no loss.
}
```

### Step 8 — Schedule

Monthly schedule: identifies the partition older than 18 months from `RunTime`; runs the archive workflow for it.

### Step 9 — Tests

- Unit tests for each activity using mocks (S3, Vault, MetadataStore stubs).
- Integration test using `localstack` for S3 + testcontainers PG. Verifies:
  - Partition detached.
  - Parquet file uploaded with correct schema, encrypted.
  - Manifest + checksum present.
  - Glue register succeeded (against `localstack` Glue).
  - Outbox event emitted.
  - PG partition dropped.

### Step 10 — Restore stub

File: `plane/workflow/billing/restore_partition_workflow.go`

Out-of-scope for this PR per ADR-018. Add a TODO doc.go comment + open a follow-up issue.

## Validation gates

| Gate | Command | Pass |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Lint determinism | `make lint-determinism` | clean |
| Unit | `go test -race ./plane/workflow/billing/...` | pass |
| Integration | `go test -tags integration -race ./plane/workflow/billing/...` | pass against localstack |
| Manual e2e | run against staging analytics-lake bucket | partition archived, Glue catalog updated, outbox event observed |

## Acceptance checklist

- [ ] All 5 activities implemented and idempotent.
- [ ] Parquet file uses zstd + encrypted with month-keyed DEK.
- [ ] `.manifest.json` records row count, schema version, KEK version, source partition name.
- [ ] `.checksum.sha256` written and verifiable.
- [ ] Glue catalog updated atomically with archive.
- [ ] Outbox event `billing.partition_archived` emitted via `appclient.BillingClient` (ADR-019 routing).
- [ ] PG partition dropped only after all upstream success.
- [ ] Workflow heartbeats during long Parquet write (>5min).
- [ ] PR cross-links ADR-018 + ADR-019 + spec.
- [ ] `RestorePartition` follow-up issue filed.

## Risks

| Risk | Mitigation |
|---|---|
| Parquet writer exhausts memory on 200GB partitions | streaming row batches; never load full partition into memory |
| KEK rotation mid-archive | manifest captures `kek_version`; restore uses recorded version |
| S3 multipart upload partial failure | parquet-go writer + S3 SDK handle retries; manifest is final step |
| Glue catalog write fails after Parquet upload | activity is idempotent on rerun; "dark" partition state is recoverable |
| Drop detached partition fails after archive | logs + page; data redundant, not lost |
| Vault unavailability blocks DEK derivation | activity retries with backoff; workflow surface this as a paging-grade alert after N attempts |
| Localstack-Glue divergence from real AWS | manual e2e against staging is mandatory; localstack test catches code paths only |
| Per-month DEK destruction post-7y not yet implemented | track as separate retention-enforcer follow-up workflow |

## Rollback

If a Parquet upload corrupts data, the PG partition is still detached but present (drop activity is gated on success). Manual remediation = re-attach the partition (`ALTER TABLE … ATTACH PARTITION`) — no data loss. Document this runbook in PR.
