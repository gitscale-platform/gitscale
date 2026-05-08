# Spec: #33 Workflow plane bootstrap

**Date:** 2026-05-06
**Issue:** #33 `[Workflow] Plane bootstrap — Temporal worker, namespace, task queue, registry (ADR-003)`
**Branch:** `feat/workflow-plane-bootstrap`
**Depends on:** ADR-019 (workflow→app-plane boundary) — must merge first
**Blocks:** #18 (billing partition rollover), all future workflows

## 1. Context

`plane/workflow/` is empty (only `doc.go`). #33 is the first workflow-plane code. Three workflows are queued behind it: #18 (billing partition rollover), future agent-session orchestration, future CI pipelines (Firecracker provisioning). All three need a shared bootstrap: worker entrypoint, namespace + task-queue conventions, registration scaffolding, plane-boundary adapters, determinism enforcement.

ADR-003 (Temporal) is silent on layout, registration, and queue naming — those are gap-fills locked here.

## 2. Decisions

### D1 — Temporal namespace is env-derived, not literal

**Decision.** Namespace = `gitscale-${env}` where `env ∈ {prod, staging, dev}`. Read from env var `TEMPORAL_NAMESPACE` at worker start; fail fast if unset.

**Rejected.** Single literal `gitscale` namespace. Namespaces are the retention/RBAC/archival boundary in Temporal; a single literal name forces cross-env ACLs and conflates dev history with prod history.

**Rejected.** Per-tenant namespaces. Known anti-pattern at scale — namespaces are shard-expensive. Tenant isolation lives in workflow ID prefix + search attributes (Phase-2 concern).

**Implication.** `cmd/workflow-worker/main.go` reads `TEMPORAL_NAMESPACE` from env, no default.

### D2 — Task queues by workload, not by tenant

**Decision.** Three task queues named at launch:

| Queue | Owner | Status at #33 |
|---|---|---|
| `billing-maintenance` | data-plane operations (#18 ships first) | active |
| `agent-sessions` | future agent-session workflows | constant only |
| `ci-pipelines` | future Firecracker provisioning | constant only |

Constants live in `plane/workflow/queues.go`:

```go
package workflow

const (
    QueueBillingMaintenance = "billing-maintenance"
    QueueAgentSessions      = "agent-sessions"
    QueueCIPipelines        = "ci-pipelines"
)
```

**Rationale.** Coarse-by-workload keeps Temporal's task-queue routing meaningful. Per-tenant or per-domain queues lead to queue-explosion + uneven worker load.

### D3 — No `adapters/data/` umbrella; selective wrapping

**Decision.**

- **Direct import** of `plane/data/store` and `plane/data/cache` interfaces from activity constructors. No re-export, no umbrella adapter. Architect verdict: redundant interface layer over `MetadataStore` / `CacheStore` will drift.
- **New package** `plane/workflow/appclient/` for HTTP/gRPC clients into application-plane services (e.g. `IdentityClient`). This IS a new wrapper, not a re-export, because the transport layer is genuinely workflow-plane code.

**Activity constructor shape.**

```go
type CreatePartitionActivity struct {
    metadataStore store.MetadataStore // direct interface from plane/data/store
}

type CreateUserActivity struct {
    identityClient appclient.IdentityClient // wraps gRPC into plane/application/identity
}
```

**Rationale.** Direct imports are safe because `plane/data/store` and `plane/data/cache` already define the swap surfaces (ADR-017). Re-wrapping them produces two parallel interfaces that drift. `appclient/` is justified because no equivalent interface exists in the app plane today.

**Constraint.** Activities MUST receive interfaces, never concretes. Workflow code MUST NOT import either package directly (only activities do).

### D4 — Registry by task-queue bundle

**Decision.** `plane/workflow/registry.go`:

```go
package workflow

// Bundle is the unit of registration for one task queue. A worker registers
// exactly one Bundle per queue it serves.
type Bundle struct {
    TaskQueue  string
    Workflows  []any // workflow funcs or struct-method pairs
    Activities []any
}

// Registrar is implemented by *worker.Worker.
type Registrar interface {
    RegisterWorkflow(any)
    RegisterActivity(any)
}

// Apply registers all workflows and activities in this Bundle on the given worker.
func (b Bundle) Apply(r Registrar) {
    for _, wf := range b.Workflows {
        r.RegisterWorkflow(wf)
    }
    for _, a := range b.Activities {
        r.RegisterActivity(a)
    }
}
```

`cmd/workflow-worker/main.go` collects Bundles from each domain package and applies one Bundle per queue. New workflows plug in by exporting a `Bundle()` function from their package; `main.go` pulls the bundle without modification.

**Slot for ADR-015 ApprovalActivity.** A `Bundle` from `plane/workflow/approval/` (future) registers the `ApprovalActivity` on every queue that may need plan-approval gating. No registry change required when ADR-015 lands.

### D5 — Determinism enforcement = externalised rules + lint script

**Decision.** Rules live in `plane/workflow/lint/determinism-rules.txt` (one regex per line, `#`-comments). `plane/workflow/lint/lint-determinism.sh` reads rules + greps Go files under `plane/workflow/**/workflow*.go` and `plane/workflow/**/workflows/*.go`.

**Initial rule set.**

```
# Time and randomness
\btime\.Now\(\)
\btime\.After\(
\btime\.NewTimer\(
\bmath/rand\b
\bcrypto/rand\b
\bgoogle\.uuid\.New[^V]
# I/O and config
\bos\.Getenv\(
\bnet/http\b
\bnet\.[A-Z]
# Concurrency primitives
\bsync\.[A-Z]
\bgo\s+func\(
\bmake\(chan\b
# Control flow over maps (warning, not failure, unless body has workflow.)
^\s*for\s+.*\s+:=\s+range\s+.+(?=.*workflow\.)
```

`workflow.SideEffect` and `workflow.MutableSideEffect` are the escape hatches. Activity files (`activity*.go`, `activities/*.go`) are exempt — activities are the I/O boundary per ADR-003.

**CI step.** `.github/workflows/go.yml` adds `make lint-determinism` (config + script committed in same PR — CLAUDE.md CI linter rule).

**Test fixture.** `plane/workflow/lint/testdata/bad/forbidden_time_now.go` and friends exist; integration test runs `lint-determinism.sh` against them and asserts non-zero exit. Proves the lint isn't silently passing.

### D6 — Schedule API, not cron string

**Decision.** Workflows wanting cron behaviour (#18) use Temporal's Schedule API (`client.ScheduleClient().Create(...)`), not the legacy `cron_schedule` workflow option.

**Rationale.** Schedule API is discoverable (`tctl schedule list`), pausable, supports backfill, and survives namespace migrations. Cron strings are opaque and untyped.

**Implementation.** Helper `plane/workflow/schedule.go`:

```go
func EnsureSchedule(ctx context.Context, c client.Client, spec ScheduleSpec) error
```

Idempotent: creates if absent, updates if drifted.

### D7 — OTel interceptor wired in registry

**Decision.** `cmd/workflow-worker/main.go` builds the worker with `temporal.io/sdk/contrib/opentelemetry` interceptors so workflow + activity spans are emitted with proper parent-child linkage.

**Resource attributes.**

| Attr | Value |
|---|---|
| `service.name` | `gitscale-workflow-worker` |
| `service.namespace` | `gitscale-${env}` |
| `service.instance.id` | hostname or k8s pod name |

OTLP endpoint via env var `OTEL_EXPORTER_OTLP_ENDPOINT`.

### D8 — Worker options pinned + env-tunable

**Decision.** Defaults in code; override via env.

| Option | Default | Env |
|---|---|---|
| `MaxConcurrentActivityExecutionSize` | 100 | `WORKER_MAX_CONCURRENT_ACTIVITIES` |
| `MaxConcurrentWorkflowTaskPollers` | 4 | `WORKER_WORKFLOW_POLLERS` |
| `MaxConcurrentActivityTaskPollers` | 4 | `WORKER_ACTIVITY_POLLERS` |
| `WorkerStopTimeout` | 30s | `WORKER_STOP_TIMEOUT` |
| `EnableSessionWorker` | false | — |

### D9 — Default RetryPolicy per activity

**Decision.** No activity uses Temporal's default unlimited retries. Helper:

```go
package workflow

func DefaultRetryPolicy() *temporal.RetryPolicy {
    return &temporal.RetryPolicy{
        InitialInterval:    1 * time.Second,
        BackoffCoefficient: 2.0,
        MaximumInterval:    60 * time.Second,
        MaximumAttempts:    5,
        // NonRetryableErrorTypes: per-activity, set in ActivityOptions
    }
}
```

Workflows that want different policy override per-activity in `ActivityOptions`. `CreatePartition` (#18) explicitly sets `MaximumAttempts: 5` — DDL retries should not loop forever.

### D10 — Single-domain activity rule

**Decision.** No activity writes more than one domain's outbox row. Cross-domain workflows compose per-domain saga steps with explicit compensation.

**Enforcement.** Code review only at this stage (no static check). Documented in `plane/workflow/README.md` and ADR-019.

### D11 — `workflow.GetVersion` + continue-as-new helpers land in #33

**Decision.** Project-wide constants:

```go
package workflow

const (
    // ContinueAsNewThreshold is the default event-history size threshold
    // beyond which a long-running workflow should call ContinueAsNew.
    ContinueAsNewThreshold = 8000
)

// ShouldContinueAsNew is the canonical check; long workflows call it after each step.
func ShouldContinueAsNew(ctx workflow.Context) bool {
    info := workflow.GetInfo(ctx)
    return info.GetCurrentHistoryLength() >= ContinueAsNewThreshold
}
```

`#18` does not need this (single-shot per cron firing) but the helper lands in #33 so future agent-session workflows have a default ready.

### D12 — Canary workflow exercises one read-only activity through real adapter

**Decision.** Canary is NOT no-op. Shape:

```go
// plane/workflow/canary/workflow.go
func HealthRoundTripWorkflow(ctx workflow.Context) (string, error) {
    var got string
    err := workflow.ExecuteActivity(ctx, ReadHealthKeyActivity).Get(ctx, &got)
    return got, err
}
```

`ReadHealthKeyActivity` does `cache.Get(ctx, "gitscale:${env}:workflow:health")`. Integration test:

1. Spin Temporal dev server + Redis testcontainer.
2. Set the health key directly.
3. Trigger the canary workflow via `client.ExecuteWorkflow`.
4. Assert returned value matches.

This proves: registry wiring, queue routing, adapter wiring (cache import), OTel propagation, env-derived namespace, worker options. Bare no-op proves only that the worker starts.

## 3. Files to add

```
cmd/workflow-worker/main.go
plane/workflow/queues.go
plane/workflow/registry.go
plane/workflow/schedule.go
plane/workflow/retrypolicy.go
plane/workflow/continueasnew.go
plane/workflow/appclient/identity.go         # interface only at #33; impl follows #15-revocation
plane/workflow/appclient/doc.go
plane/workflow/lint/determinism-rules.txt
plane/workflow/lint/lint-determinism.sh
plane/workflow/lint/testdata/bad/forbidden_time_now.go
plane/workflow/lint/testdata/bad/forbidden_uuid_new.go
plane/workflow/lint/testdata/good/clean_workflow.go
plane/workflow/canary/workflow.go
plane/workflow/canary/activity.go
plane/workflow/canary/bundle.go
plane/workflow/canary/integration_test.go
plane/workflow/README.md                     # documents D1–D12
.github/workflows/go.yml                     # adds make lint-determinism
Makefile                                     # adds lint-determinism target
```

## 4. Files to modify

| Path | Change |
|---|---|
| `Makefile` | add `lint-determinism` target |
| `.github/workflows/go.yml` | add CI step running `make lint-determinism` |
| `.env.example` | add `TEMPORAL_NAMESPACE`, `TEMPORAL_HOST`, `WORKER_*` defaults |
| `docker-compose.yml` | add Temporal dev server (port 7233) for local dev |

## 5. Implementation plan

### Phase A — boilerplate (no Temporal deps)

1. `plane/workflow/queues.go` — task queue constants.
2. `plane/workflow/registry.go` — `Bundle` + `Registrar` + `Apply`.
3. `plane/workflow/retrypolicy.go` — `DefaultRetryPolicy`.
4. `plane/workflow/continueasnew.go` — threshold + `ShouldContinueAsNew`.
5. `plane/workflow/lint/determinism-rules.txt` + `lint-determinism.sh` + testdata.
6. `Makefile` lint target + CI step.
7. Verify `make lint-determinism` exits 1 on bad fixtures, 0 on good.

### Phase B — Temporal worker

8. `go.mod`: add `go.temporal.io/sdk` + `go.temporal.io/sdk/contrib/opentelemetry`.
9. `cmd/workflow-worker/main.go` — Temporal client, namespace from env, OTel interceptors, signal handling, graceful `worker.Stop()`.
10. `plane/workflow/schedule.go` — `EnsureSchedule` helper.
11. `.env.example` + `docker-compose.yml` — local dev wiring.

### Phase C — appclient interface

12. `plane/workflow/appclient/identity.go` — `IdentityClient` interface (impl deferred to #15-revocation Wave; stub returns `errNotImplemented`).

### Phase D — canary

13. `plane/workflow/canary/{workflow,activity,bundle}.go`.
14. `plane/workflow/canary/integration_test.go` — Temporal dev server + Redis testcontainer.
15. `cmd/workflow-worker/main.go` registers the canary bundle on `QueueBillingMaintenance`.

### Phase E — docs

16. `plane/workflow/README.md` — document D1–D12, plane boundary, single-domain rule, registry conventions.
17. PR description includes ADR-019 cross-link + acceptance-criteria checklist.

## 6. Acceptance criteria

- [ ] `cmd/workflow-worker` builds; binary boots with `TEMPORAL_NAMESPACE=gitscale-dev` against docker-compose Temporal.
- [ ] Canary integration test passes (real Redis testcontainer, Temporal dev server).
- [ ] `make lint-determinism` is wired into CI and demonstrably fails on bad fixtures.
- [ ] `plane/workflow/README.md` documents D1–D12.
- [ ] No `plane/workflow/adapters/data/` package exists.
- [ ] `appclient.IdentityClient` interface compiles; impl stub returns sentinel.
- [ ] Worker stops gracefully on SIGTERM (in-flight activities drain within `WorkerStopTimeout`).

## 7. Open items deferred to follow-up issues

- **`appclient.IdentityClient` gRPC implementation** — depends on #15-revocation defining the gRPC surface. Tracked under #15-revocation.
- **`agent-sessions` and `ci-pipelines` queue workflows** — out of scope; constants only at #33.
- **Tenant isolation via search attributes** — Phase-2.
- **Replica coordination** — Temporal handles this natively via task queues; no custom sharding.

## 8. Risk mitigations

| Risk | Mitigation |
|---|---|
| Determinism lint regex set incomplete | Externalized rules file; new rules added without script change |
| Worker config skew between dev / staging / prod | All knobs env-tunable; defaults pinned in code; `.env.example` is the contract |
| OTel collector mis-configured silently | Worker logs OTLP target at startup; alarm on missing spans in canary integration test |
| `appclient` stub merged but no impl follow-up | Issue #33 explicitly cross-links the gRPC follow-up under #15-revocation |
| Temporal dev server flake in CI | Use `go.temporal.io/sdk/testsuite` time-skipping environment for unit tests; only canary integration test boots the dev server |

## 9. Cross-references

- ADR-003 (Temporal orchestration) — governs determinism, activity-as-IO-boundary.
- ADR-008 (outbox) — D10 single-domain rule preserves transactional outbox invariant.
- ADR-015 (plan approval) — D4 registry leaves slot for `ApprovalActivity`.
- ADR-017 (interface swap surface) — D3 direct-import of `plane/data/store` + `plane/data/cache` interfaces.
- ADR-019 (workflow→app-plane boundary, this PR cycle) — formalises D3 + D10.
- `gitscale-temporal-determinism` skill — canonical rule reference for D5.
- `gitscale-plane-boundary` skill — enforces D3 import constraints.
