# Spec — Issue #79 RestorePartition workflow

Date: 2026-05-08
Issue: #79
Plane: workflow
Priority: p3 (Wave 2; deps #76)
ADR-impact: conforming (ADR-018 §Restore path)

## Goals

1. Restore an archived monthly partition into a quarantine table for
   dispute-investigation / audit-restore.
2. Decode the `aes-256-gcm-v1-4mib` chunked frame format (inverse of
   ExportActivity's encrypt loop) with AAD validation per chunk.
3. Idempotent on `(year, month)` via Temporal workflow ID.
4. Round-trip integration test: archive then restore; row sets match by
   `(id, ts)`.

## Non-goals

- Re-attaching the restored partition to live `billing.usage_events`
  (quarantine-only by design).
- Restoring multiple months in a single workflow run.
- Restoring archives with `enc_format != aes-256-gcm-v1-4mib` (reject and
  log; no compatibility shim).

## Architecture

### Workflow

`plane/workflow/billing/restore_workflow.go`:

```go
const RestorePartitionWorkflowName = "billing.RestorePartitionWorkflow"

type RestoreInput struct {
    Year  int
    Month int
}

type RestoreResult struct {
    QuarantineTable string
    RowsImported    int64
    DEKVersionUsed  int    // parsed from manifest.kek_hint
}

func RestorePartitionWorkflow(ctx workflow.Context, in RestoreInput) (RestoreResult, error)
```

Activity sequence (sequential):

1. `FetchManifestActivity` — `S3ObjectStore.GetBytes(<base>.manifest.json)`
2. `VerifyChecksumActivity` — recompute SHA-256 of `<base>.parquet`,
   compare to `<base>.checksum.sha256`
3. `DownloadAndDecryptActivity` — pull encrypted parquet, dispatch on
   `manifest.enc_format`, decrypt chunked frames with AAD
   `<source_partition>:<chunk_index>`, write plaintext parquet to scratch
4. `LoadIntoQuarantineActivity` — `CREATE TABLE
   billing.usage_events_restore_YYYY_MM (LIKE billing.usage_events
   INCLUDING DEFAULTS); COPY FROM` plaintext parquet rows; mark table as
   read-only via `REVOKE INSERT/UPDATE/DELETE`

Compensation: scratch-file delete + quarantine-table-drop on workflow
failure (registered as `defer activity.RegisterCompensation`).

### Frame decoder

In `plane/workflow/billing/restore_decoder.go`:

```go
type ChunkedDecoder struct {
    DEK []byte // 32 bytes
}

// DecodeStream reads the encrypted chunked stream, validates AAD per
// chunk against partitionName + chunk index, and writes plaintext to w.
func (d *ChunkedDecoder) DecodeStream(r io.Reader, w io.Writer, partitionName string) error
```

Mirror exactly the encrypt loop in `export_activity.go`. Reject malformed
chunks with `ErrFrameTampered`. Reject manifest formats other than
`aes-256-gcm-v1-4mib` with `ErrUnsupportedEncFormat`.

### KeyProvider integration

`DownloadAndDecryptActivity` resolves the DEK via `KeyProvider.GetDEK(year,
month)`. The returned `DEK.KEKHint` must equal `manifest.kek_hint`; if not,
the worker is using a different Vault key version than was used to
encrypt — the activity surfaces an error directing the operator to
configure the historical key version (Vault transit retains versions on
rotation; pass version explicitly via a future `KeyProviderVersioned`
seam — out of scope for this PR; document in the runbook).

### Bundle + worker wiring

`Bundle.RestoreDeps` (new field) holds the four activities;
`Bundle.Apply` registers `RestorePartitionWorkflow` and the four activities
on `QueueBillingMaintenance`.

`cmd/workflow-worker/main.go` constructs the deps in the same env-gated
archive block (#76).

### Integration test

`plane/workflow/billing/restore_workflow_integration_test.go`
(`//go:build integration`):

- Boot PG + minio + Vault.
- Run `PartitionArchiveWorkflow{Year:2024, Month:5}` to seed an archive.
- Run `RestorePartitionWorkflow{Year:2024, Month:5}`.
- Assert: `billing.usage_events_restore_2024_05` exists; row count matches
  pre-archive seed; SELECT * ordered by `(id, ts)` from quarantine table
  matches a snapshot taken before the archive ran.

## Acceptance checklist

- [ ] Workflow + 4 activities registered on QueueBillingMaintenance
- [ ] `restore_decoder.go` is the inverse of `export_activity.go` encrypt loop
- [ ] Round-trip integration test passes
- [ ] Manual operator runbook in `docs/runbooks/billing-partition-restore.md`
- [ ] PR description references ADR-018

## References

- ADR-018 §Restore path
- Source format: `plane/workflow/billing/export_activity.go`
- Spec parent: `docs/superpowers/specs/2026-05-07-issue-69-archive-workflow-design.md`
