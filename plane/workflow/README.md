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

## Surface

The plane shipped in two PRs to confine the Temporal SDK dep upgrade:

**Phase A (#56)** — Go-stdlib-only scaffolding: `queues.go`, `registry.go`,
determinism rules + lint script + CI job, testdata fixtures.

**Phase B (#33-B)** — Temporal SDK integration:

- `DefaultRetryPolicy()` (`retrypolicy.go`) — 5 attempts, 1s→60s backoff;
  every activity inherits this unless its `ActivityOptions` override.
- `ShouldContinueAsNew(ctx)` (`continueasnew.go`) — caps a single run's
  history at 50000 events; long-lived workflows must check this in their
  loop and `workflow.NewContinueAsNewError` when true.
- `NamedActivity` extension to `Bundle` so activities can register with an
  explicit string name; workflows dispatch by name without holding a
  reference to the activity instance.
- `cmd/workflow-worker/main.go` — env-driven Temporal client, worker options
  pinned per spec D8 (max activities, poller counts, stop timeout),
  SIGTERM-trapped graceful shutdown.
- `appclient/` — `IdentityClient` interface + stub impl. The gRPC impl
  ships in #15-revocation under `appclient/identity_grpc.go`.
- `canary/` — minimal workflow + read-only activity that the worker boots
  with. Integration test uses `go.temporal.io/sdk/testsuite` (no real
  Temporal server needed in CI).

Deferred to a separate follow-up issue:

- OTel interceptor + resource attributes (spec D7).
- `EnsureSchedule` helper for Temporal Schedule API (spec D6).
- `docker-compose.yml` Temporal dev-server entry + `.env.example`.
- Full schedule integration once #18-rollover lands.

## Cross-references

- ADR-003 — Temporal as the orchestration layer.
- ADR-008 — outbox-based event consistency.
- ADR-019 — workflow→app-plane boundary.
- ADR-015 — plan approval / risky-action policy. The registry has an
  implicit slot for a future `ApprovalActivity` Bundle that registers on
  every queue requiring plan-approval gating.
- `gitscale-temporal-determinism` skill, `gitscale-plane-boundary` skill —
  enforcement at the editor.
