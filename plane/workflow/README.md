# Workflow plane

GitScale's workflow plane runs Temporal-orchestrated, durable, long-running
workflows: billing partition rollover (#18), agent-session lifecycle, CI
pipelines (Firecracker microVM provisioning). This README documents the
conventions every workflow PR must follow.

## Plane-boundary rule (ADR-019)

Workflow activities **must not** call `MetadataStore.Transact` for state
mutations. All domain writes route through `plane/workflow/appclient/<domain>`
gRPC clients into the application plane, which performs the Tx + outbox
write under uniform auth/audit context (ADR-008 + ADR-010).

Two exceptions:

1. **Read-only activities** MAY use `plane/data/store` and `plane/data/cache`
   interfaces directly. A method that does not call `Transact` or
   `WriteOutbox` is read-only.
2. **Pure-DDL maintenance activities** (e.g. `CreatePartition` in #18) MAY
   use `MetadataStore` directly because the operation has no outbox row and
   no domain invariant.

Cross-domain workflows compose per-domain saga steps with explicit
compensation activities. No workflow activity writes more than one domain's
outbox row.

## Task queues

Constants in [`queues.go`](./queues.go). Three queues at launch:

| Queue | Owner |
|---|---|
| `billing-maintenance` | data operations (#18 ships first) |
| `agent-sessions` | agent runtime (Phase 2) |
| `ci-pipelines` | Firecracker provisioning (Phase 2) |

Routing is coarse-by-workload — never per-tenant. Tenant isolation is
workflow-id-prefix + search-attributes territory.

## Bundle registry

Each domain package exports a `Bundle()` function returning a `Bundle{TaskQueue, Workflows, Activities}`. The worker entrypoint
(`cmd/workflow-worker/main.go`, ships in a follow-up PR with the Temporal
SDK wiring) collects bundles and applies one per worker. Adding a workflow
requires no change to `main.go`.

```go
// plane/workflow/billing/partitionrollover/bundle.go (sketch — #18)
func Bundle(deps Deps) workflow.Bundle {
    return workflow.Bundle{
        TaskQueue:  workflow.QueueBillingMaintenance,
        Workflows:  []any{PartitionRolloverWorkflow},
        Activities: []any{deps.CreatePartition},
    }
}
```

## Determinism enforcement

Workflow code runs deterministically across replays — no `time.Now()`, no
`go func()`, no map-range with side effects, no I/O, no environment reads.

Rules live in [`lint/determinism-rules.txt`](./lint/determinism-rules.txt)
(one regex per line). The script
[`lint/lint-determinism.sh`](./lint/lint-determinism.sh) reads them and
greps every `workflow*.go` and `workflows/*.go` under `plane/workflow/`.
Activity files (`activity*.go`) and test files (`*_test.go`) are exempt —
activities are the I/O boundary per ADR-003.

Run locally:

```sh
make lint-determinism
```

CI runs the same step in a dedicated job (`.github/workflows/go.yml`).
The lint contract is verified by an integration test that points the
script at `lint/testdata/bad/` (must fail) and `lint/testdata/good/`
(must pass).

Escape hatches: `workflow.SideEffect` and `workflow.MutableSideEffect` for
sources of non-determinism that must be captured into history.

## What ships in this PR vs. follow-ups

This PR (#33) ships the structure that does not depend on the Temporal SDK:

- task-queue constants (`queues.go`)
- bundle registry (`registry.go`) with a `Registrar` interface satisfied by
  `*worker.Worker` once the SDK is wired in
- determinism rules + lint script + Makefile target + CI job + testdata
  fixtures
- this README

The Temporal SDK wiring — `cmd/workflow-worker/main.go`, OTel interceptor,
worker options pinning, `EnsureSchedule` helper, `DefaultRetryPolicy`,
`ShouldContinueAsNew`, `appclient.IdentityClient` interface, canary
workflow + integration test — ships in a follow-up issue. The split keeps
this PR reviewable and unblocks #18-rollover (which uses the queue
constants and bundle registration model from here) without bringing in
the full Temporal dep tree.

## Cross-references

- ADR-003 — Temporal as the orchestration layer.
- ADR-008 — outbox-based event consistency.
- ADR-019 — workflow→app-plane boundary.
- ADR-015 — plan approval / risky-action policy. The registry has an
  implicit slot for a future `ApprovalActivity` Bundle that registers on
  every queue requiring plan-approval gating.
- `gitscale-temporal-determinism` skill, `gitscale-plane-boundary` skill —
  enforcement at the editor.
