# Issue #80 DEK destruction workflow — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Implement `DEKDestructionWorkflow` that destroys per-month Vault transit key versions for archives ≥ 7y + 30d old, with legal-hold + operator-approval gates, idempotent on `(year, month)`.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-80-dek-destruction-design.md`

**Branch:** `feat/workflow-dek-destruction`

---

## File map

### Create
- `plane/workflow/billing/dek_destruction_workflow.go`
- `plane/workflow/billing/dek_destruction_router_workflow.go`
- `plane/workflow/billing/dek_destruction_workflow_test.go`
- `plane/workflow/billing/list_eligible_partitions_activity.go`
- `plane/workflow/billing/check_legal_hold_activity.go`
- `plane/workflow/billing/request_operator_approval_activity.go`
- `plane/workflow/billing/destroy_dek_activity.go`
- `plane/workflow/billing/emit_dek_destroyed_activity.go`
- `plane/workflow/billing/dek_destruction_schedules.go`
- `plane/workflow/billing/dek_destruction_workflow_integration_test.go`
- `docs/runbooks/billing-dek-destruction.md`

### Modify
- `plane/workflow/billing/bundle.go` — `DEKDestructionDeps` field; register
- `cmd/workflow-worker/main.go` — wire deps + schedule in env-gated block (#76)
- `plane/application/billing/events.go` — add `EventTypePartitionDEKDestroyed` constant + payload
- `plane/application/billing/postgres_service.go` — handler for new event type (delegates to outbox write only — no source row needed; the destruction itself is the side effect)

---

## Tasks (compressed)

1. **Schema-side payload + service:** add `partition_dek_destroyed` event type and a thin `RecordDEKDestroyed` RPC in `plane/application/billing` that writes only an outbox row (no source-row update — the destruction is recorded by absence of the key, plus the audit event).
2. **List/check/approve activities:** unit-test each with stubs.
3. **DestroyDEK activity:** Vault `transit/keys/<key>/<version>/destroy`. Idempotent: 404 returns nil. Unit-test against fake `vault.Client`.
4. **EmitDEKDestroyed activity:** calls `BillingClient.RecordDEKDestroyed` (new RPC).
5. **Workflow body:** deterministic iteration over eligible partitions; per-partition activity chain; `Result.Skipped` accumulation. Workflow testsuite verifies happy + each skip path (held / rejected / vault-error).
6. **Router workflow + schedule:** mirrors #76's ArchiveRouter pattern.
7. **Worker wiring:** mirror #76 env-gated block.
8. **Integration test:** Vault testcontainer with versioned key; assert version destroyed + outbox row written.
9. **Runbook:** docs/runbooks/billing-dek-destruction.md.
10. **Final gates:** test sweep, skills (`gitscale-temporal-determinism`, `gitscale-go-conventions`, `gitscale-plane-boundary`, `gitscale-outbox-check`, `gitscale-adr-guard`), self-review battery, push, PR. Closes #80.
