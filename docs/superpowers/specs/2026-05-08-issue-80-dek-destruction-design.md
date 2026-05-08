# Spec — Issue #80 Per-month DEK destruction workflow

Date: 2026-05-08
Issue: #80
Plane: workflow
Priority: p3 (Wave 2; deps #76, soft-gates on #75 KeyProvider)
ADR-impact: conforming (ADR-018 §Encryption / crypto-shred; ADR-015 operator
approval)

## Goals

1. Monthly scheduled workflow that identifies partitions ≥ 7y + 30d old
   and destroys their per-month Vault transit key version (crypto-shred).
2. Pre-flight legal-hold check (S3 prefix lifecycle status).
3. Operator-approval gate (per ADR-015) before the irreversible
   `vault transit/keys/<key>/<version>/destroy` call.
4. Outbox event `billing.partition_dek_destroyed` for audit trail.
5. Idempotent on `(year, month)` via Temporal workflow ID.

## Non-goals

- Modifying the legal-hold mechanism itself (assumes S3 Object Lock or a
  Legal-Hold prefix is already configured at the bucket level).
- Destroying entire transit keys (only specific versions per ADR-018).
- Restoring a destroyed DEK (impossible by design).

## Architecture

### Workflow

`plane/workflow/billing/dek_destruction_workflow.go`:

```go
const DEKDestructionWorkflowName = "billing.DEKDestructionWorkflow"

type DEKDestructionInput struct {
    RunTime time.Time // bound at fire time by the schedule (cmd-level)
}

type DEKDestructionResult struct {
    PartitionsScanned int
    KeysDestroyed     int
    Skipped           []string // reason per skip
}

func DEKDestructionWorkflow(ctx workflow.Context, in DEKDestructionInput) (DEKDestructionResult, error)
```

Activity sequence:

1. `ListEligiblePartitionsActivity(ctx, cutoff)` — scans
   `billing.partition_archives` for rows where `archived_at < now() - 7y -
   30d`. Returns `[]PartitionArchive`.
2. `CheckLegalHoldActivity(ctx, lakeURI)` — calls S3 GetObjectLockConfiguration
   on the prefix; returns the legal-hold state. Skip the partition if
   held; record reason.
3. `RequestOperatorApprovalActivity(ctx, partitionList)` — uses the existing
   ADR-015 approval mechanism (or stub for now; spec D9). On reject, abort
   the run for that partition.
4. `DestroyDEKActivity(ctx, year, month, kekHint)` — parses the version
   from `kekHint` (`platform-billing-v<N>`) and calls Vault transit
   `keys/<keyName>/<version>/destroy`. Idempotent: a missing version
   returns success.
5. `EmitDEKDestroyedEventActivity(ctx, partition)` — writes
   `billing.partition_dek_destroyed` to billing_outbox via the
   `BillingClient` gRPC (per ADR-019 boundary).

The workflow body iterates the partition list deterministically; one
activity sequence per eligible partition. Errors per partition do not
abort the whole run — they are accumulated in `Result.Skipped`.

### Schedule

Monthly cron via `EnsureDEKDestructionSchedule(ctx, sc)`. Uses an
`ArchiveRouterWorkflow`-style dynamic args computation via a thin router
workflow that calls `workflow.Now(ctx)` and spawns the actual workflow
with `RunTime` bound — same trick as #76's `ArchiveRouterWorkflow`.

### Bundle + worker wiring

`Bundle.DEKDestructionDeps`; register on `QueueBillingMaintenance`. Worker
constructs activities in the env-gated archive block.

### Integration test

`plane/workflow/billing/dek_destruction_workflow_integration_test.go`
(`//go:build integration`):

- Boot Vault testcontainer with a versioned `platform-billing-master`.
- Pre-create transit key + rotate to v2.
- Seed `billing.partition_archives` row with `archived_at < cutoff`,
  `kek_hint = "platform-billing-v1"`.
- Run the workflow; auto-approve via stub.
- Assert: Vault `key_version=1` is in `archive_keys` (destroyed), and the
  outbox row exists.

### Runbook

`docs/runbooks/billing-dek-destruction.md`:

- Pre-flight: confirm legal-hold review queue is empty.
- Monthly schedule cadence (1st of month at 02:00 UTC).
- Operator-approval mechanism: how to approve / reject per partition.
- Legal-hold override: how to add a partition to the hold list (delays
  destruction).
- Recovery: NONE — once destroyed, the archive is unrecoverable. This is
  the design intent.

## Acceptance checklist

- [ ] Workflow + 5 activities + router on `QueueBillingMaintenance`
- [ ] Schedule registered via `EnsureDEKDestructionSchedule`
- [ ] Operator-approval gate present (real or ADR-015 stub)
- [ ] Outbox event emitted on success
- [ ] testcontainers Vault integration test asserts versioned destroy
- [ ] Runbook documents legal-hold override
- [ ] PR description references ADR-018 + ADR-015

## References

- ADR-018 §Encryption / crypto-shred (`docs/architecture.md` line ~663)
- ADR-015 (operator approval)
- `plane/workflow/billing/vault_keyprovider.go` (#75)
- `plane/workflow/billing/archive_router_workflow.go` (#76 pattern)
