# Runbook: Billing DEK destruction (crypto-shred)

> Issue #80, ADR-018 §Encryption, ADR-015 (operator approval).

## Summary

`DEKDestructionWorkflow` (workflow plane, task queue
`billing-maintenance`) destroys per-month Vault transit key versions for
billing partition archives that are at least **7 years + 30 days** old.
The destruction is irreversible by design — once the key version is
trimmed, the encrypted parquet on the analytics lake can never be
decrypted again.

The workflow runs monthly: `DEKDestructionRouterWorkflow` is the
schedule target (cron `0 2 1 * *` UTC) and computes the cutoff at fire
time, then spawns `DEKDestructionWorkflow` as a child.

## Pre-flight

Before each scheduled run:

1. Confirm the legal-hold review queue is empty. Any partition under a
   hold MUST be excluded; see [Legal-hold override](#legal-hold-override).
2. Confirm the Vault transit key `platform-billing-master` has
   `deletion_allowed=true` and `min_decryption_version` is monotonic
   non-decreasing. The activity bumps `min_decryption_version` and then
   trims; both fail if `deletion_allowed=false`.
3. Confirm the operator-approval channel is staffed (see
   [Operator approval](#operator-approval)).

## Schedule cadence

- Schedule ID: `billing-dek-destruction`
- Cron: `0 2 1 * *` (UTC) — 1st of every month at 02:00 UTC
- Retention: 7 years + 30 days (`DEKDestructionRetentionDays = 365*7 + 30`)
- Task queue: `billing-maintenance`

The 02:00 UTC fire time is chosen to land well after the 24th-of-month
partition rollover (12:00 UTC) and archive (14:00 UTC) schedules so any
prior cleanup work has settled.

## Operator approval

Per ADR-015, every irreversible action gates on operator approval. The
`RequestOperatorApprovalActivity` boundary takes an `OperatorApprover`
implementation; production wiring depends on the ADR-015 mechanism
landing.

**Current state (until ADR-015 is implemented):**
`cmd/workflow-worker` wires `AutoApproveStub`, which auto-approves every
request and emits a structured warning log:

```text
DEK-destruction operator-approval auto-approved (ADR-015 stub)
  year=2027 month=1 partition_name=billing.usage_events_2027_01
  kek_hint=platform-billing-v3
```

**Operator mitigation while the stub is in use:**

- Pause the schedule before each natural fire window:
  `temporal schedule pause --schedule-id billing-dek-destruction
  --reason "manual review window"`
- Review the candidate set out-of-band; resume only after sign-off:
  `temporal schedule unpause --schedule-id billing-dek-destruction`
- Audit emit events after each run by querying
  `billing.billing_outbox WHERE event_type = 'billing.partition_dek_destroyed'`.

**Replacement plan:** swap `AutoApproveStub` for the real ADR-015
approver in `cmd/workflow-worker/main.go`. The boundary
`OperatorApprover` interface stays the same; no workflow change required.

## Legal-hold override

`CheckLegalHoldActivity` consults a `LegalHoldChecker`. Production wiring
is staged behind a static-not-held checker
(`NewStaticLegalHoldChecker(false, "")`) until the S3-Object-Lock-backed
implementation lands.

**To add a partition to the hold list (delays destruction):**

Until the real checker ships, hold a partition by **pausing the
schedule** before the run. Once the real checker is wired (S3 Object
Lock or a dedicated legal-hold metadata table), the override path is to
flip the corresponding object's lock state at the bucket level; the
workflow will record the hold in `Result.Skipped` as
`<year>-<month>-<partition_name>: legal_hold:<reason>`.

## Recovery

**There is no recovery.** Crypto-shred is irreversible by design. Once
`min_available_version` advances past a target version, the DEK
material is permanently deleted from Vault. The encrypted parquet and
its manifest remain on the analytics lake for forensic reference, but
the ciphertext is unrecoverable.

If a destruction was made in error:

1. The audit event lives in `billing.billing_outbox` with payload
   `{kek_hint, vault_key_version, destroyed_at, ...}`. Reconstruct the
   incident timeline from there.
2. The encrypted parquet remains at the lake URI; a future legal review
   can reference what was held but not what it contained.
3. File a postmortem; tighten the operator-approval gate.

## Workflow result schema

`DEKDestructionResult.Skipped` is a per-partition string slice. Each
entry is `<YYYY-MM-partition_name>: <reason>`. Reasons:

| Reason | Meaning |
|---|---|
| `missing_kek_hint` | Manifest could not be resolved or had no `kek_hint`; backfill the manifest. |
| `legal_hold:<r>` | Partition held; skipped until the hold is lifted. |
| `legal_hold_error:<e>` | Hold check itself errored; investigate. |
| `approval_rejected:<r>` | Operator rejected the partition; will retry on the next run. |
| `approval_error:<e>` | Approval boundary errored; investigate. |
| `destroy_error:<e>` | Vault trim failed; activity will retry per `DefaultRetryPolicy`, then surface here. |
| `emit_error:<e>` | Destruction succeeded but the audit event did not land. **`KeysDestroyed` still increments** because the irreversible side effect happened — manually replay the emit via direct gRPC call or DB insert. |

## Related

- ADR-018 §Encryption — crypto-shred mandate
- ADR-015 — operator approval for irreversible actions
- ADR-008 — outbox pattern (audit event)
- ADR-019 — application-plane state-mutation boundary
- Source: `plane/workflow/billing/dek_destruction_workflow.go`
- Schedule: `plane/workflow/billing/dek_destruction_schedules.go`
