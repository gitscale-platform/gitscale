# Issue #79 RestorePartition workflow — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Implement `RestorePartitionWorkflow` (FetchManifest → VerifyChecksum → DownloadAndDecrypt → LoadIntoQuarantine), with a chunked-frame decoder that mirrors `ExportActivity`'s encrypt loop exactly. Restore writes to a quarantine table — never the live partition tree.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-79-restore-partition-design.md`

**Branch:** `feat/workflow-restore-partition`

---

## File map

### Create
- `plane/workflow/billing/restore_workflow.go`
- `plane/workflow/billing/restore_workflow_test.go`
- `plane/workflow/billing/restore_decoder.go`
- `plane/workflow/billing/restore_decoder_test.go`
- `plane/workflow/billing/fetch_manifest_activity.go`
- `plane/workflow/billing/verify_checksum_activity.go`
- `plane/workflow/billing/download_decrypt_activity.go`
- `plane/workflow/billing/load_quarantine_activity.go`
- `plane/workflow/billing/restore_workflow_integration_test.go`
- `docs/runbooks/billing-partition-restore.md`

### Modify
- `plane/workflow/billing/bundle.go` — `RestoreDeps` field; register workflow + activities
- `cmd/workflow-worker/main.go` — wire RestoreDeps in env-gated archive block (introduced by #76)

---

## Tasks (compressed)

1. **Decoder + tests:** unit-test the inverse of the encrypt loop. Use existing fixture from export tests (encrypt with stub key, then decode and compare plaintext).
2. **Activities + tests:** each activity gets a unit test using stubs for `S3ObjectStore`, `KeyProvider`, and a Postgres pool stub.
3. **Workflow + testsuite:** sequence the four activities; testsuite verifies happy + each failure mode (manifest missing, checksum mismatch, unsupported `enc_format`, COPY FROM error).
4. **Bundle + worker wiring:** mirror #76's pattern.
5. **Integration round-trip test:** archive then restore against PG + minio + Vault testcontainers; assert row sets match by `(id, ts)`.
6. **Runbook:** document operator steps; flag the historical-key-version limitation explicitly.
7. **Final gates:** test sweep, skills (`gitscale-temporal-determinism`, `gitscale-go-conventions`, `gitscale-plane-boundary`, `gitscale-adr-guard`), self-review battery, push, PR. Closes #79.

(Each task in the worktree should follow TDD: failing test → minimal impl → passing test → commit. Frame decoder is the largest single piece; budget for ~half its time on round-trip validation.)
