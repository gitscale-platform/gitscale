# Spec — Issue #78 PartitionArchiveWorkflow integration test

Date: 2026-05-08
Issue: #78
Plane: workflow
Priority: p2 (Wave 2; deps #76)
ADR-impact: none

## Goals

End-to-end integration test for `PartitionArchiveWorkflow` against
testcontainers PG 16 + minio, exercising the full activity chain
(detach → export → emit → drop) with assertions on PG state, S3 objects,
manifest, and checksum.

## Non-goals

- Refactoring the existing unit tests.
- Adding chaos / network-partition tests.

## Architecture

New file: `plane/workflow/billing/archive_workflow_integration_test.go`
(`//go:build integration`).

Test boots:
- PG 16 testcontainer with migrations applied through latest;
- minio testcontainer (S3-compatible);
- Recording `BillingClient` stub (in-process; no gRPC needed for this test);
- `StubKeyProvider`;
- Real `PostgresArchiver`, real `S3ObjectStore`.

Test seeds an old partition `usage_events_2024_05` with N rows, runs
`PartitionArchiveWorkflow{Year:2024, Month:5}` through the testsuite, then
asserts:

- PG: partition no longer exists (`pg_class` lookup returns 0).
- S3: keys `<base>.parquet`, `<base>.manifest.json`,
  `<base>.checksum.sha256` all present.
- Checksum: SHA-256 of the encrypted parquet bytes matches the
  `.checksum.sha256` content exactly.
- Manifest: `RowCount`, `SourcePartition`, `KEKHint`, `EncFormat` fields
  populated as expected.
- Recording `BillingClient` saw exactly one `RecordPartitionArchived` call
  with the correct `LakeURI` and `RowCount`.

Crash-resumption case: kill the export mid-stream by cancelling the
activity context; re-run the workflow; assert idempotent completion (S3
multipart upload re-attempted; final state matches non-crash baseline).

DETACH PENDING recovery: simulate by issuing `ALTER TABLE ... DETACH
CONCURRENTLY` and pausing before commit (use `pg_sleep` in a separate
session). Re-run the workflow; assert it recovers via `DETACH ...
FINALIZE` (the existing `PostgresArchiver` should already handle this).

## Test plan

- One `TestArchiveWorkflow_E2E_HappyPath`
- One `TestArchiveWorkflow_E2E_CrashResumption`
- One `TestArchiveWorkflow_E2E_DetachPendingRecovery`

## Acceptance checklist

- [ ] Single new test file under `//go:build integration`
- [ ] Runs in CI under `go test -tags integration ./...`
- [ ] Uses shared testcontainers fixtures pattern via `testing.M`
- [ ] Three test cases as above

## References

- Spec: `docs/superpowers/specs/2026-05-07-issue-69-archive-workflow-design.md`
- Existing unit tests: `plane/workflow/billing/archive_workflow_test.go`
