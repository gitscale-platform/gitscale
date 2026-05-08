# Plan: #33 — Workflow plane bootstrap

**Date:** 2026-05-06
**Issue:** #33
**Spec:** `2026-05-06-issue-33-workflow-bootstrap-design.md`
**Branch:** `feat/workflow-plane-bootstrap`
**Pre-merge of:** ADR-019 (`adr/workflow-app-plane-boundary`)
**Blocks:** #18 (rollover + archive), all future workflow PRs

## Pre-flight

- Confirm ADR-019 merged in `docs/architecture.md §8`.
- `git fetch && git checkout main && git pull`
- `git checkout -b feat/workflow-plane-bootstrap`
- Add Temporal dev server to `docker-compose.yml` (port 7233).

## Step sequence — Phase A (no Temporal deps)

### Step A1 — Queue constants

File: `plane/workflow/queues.go` per spec D2.

### Step A2 — Bundle registry

File: `plane/workflow/registry.go` per spec D4.

### Step A3 — Retry policy helper

File: `plane/workflow/retrypolicy.go` per spec D9.

### Step A4 — Continue-as-new helper

File: `plane/workflow/continueasnew.go` per spec D11.

### Step A5 — Determinism rules + lint script

Files:

- `plane/workflow/lint/determinism-rules.txt` (regex per line, # for comments)
- `plane/workflow/lint/lint-determinism.sh` (reads rules, greps `plane/workflow/**/workflow*.go` excluding activity files)
- `plane/workflow/lint/testdata/bad/forbidden_time_now.go`
- `plane/workflow/lint/testdata/bad/forbidden_uuid_new.go`
- `plane/workflow/lint/testdata/bad/forbidden_go_routine.go`
- `plane/workflow/lint/testdata/good/clean_workflow.go`

Initial rules per spec D5.

### Step A6 — Makefile + CI

File edits:

- `Makefile` — add `lint-determinism` target.
- `.github/workflows/go.yml` — add `make lint-determinism` step.

Verify: `make lint-determinism` exits 1 on bad fixtures, 0 on good.

## Step sequence — Phase B (Temporal worker)

### Step B1 — Module deps

```bash
go get go.temporal.io/sdk@latest
go get go.temporal.io/sdk/contrib/opentelemetry@latest
```

### Step B2 — Worker entrypoint

File: `cmd/workflow-worker/main.go`

- Loads `TEMPORAL_NAMESPACE`, `TEMPORAL_HOST`, `OTEL_EXPORTER_OTLP_ENDPOINT`, worker tunables.
- Builds Temporal client with OTel interceptor (per spec D7).
- Constructs worker on `QueueBillingMaintenance` initially.
- Applies `canary.Bundle()` (Phase D) once it exists.
- SIGTERM trap → `worker.Stop()` with `WORKER_STOP_TIMEOUT`.

### Step B3 — Schedule helper

File: `plane/workflow/schedule.go` per spec D6 — `EnsureSchedule` idempotent.

### Step B4 — Env + compose

Files:

- `.env.example` — add `TEMPORAL_NAMESPACE=gitscale-dev`, `TEMPORAL_HOST=localhost:7233`, etc.
- `docker-compose.yml` — add Temporal dev server service.

## Step sequence — Phase C (appclient interface)

### Step C1 — IdentityClient interface

File: `plane/workflow/appclient/identity.go`

```go
package appclient

type IdentityClient interface {
    DisableUser(ctx context.Context, id uuid.UUID, reason string) error
    RevokeAgent(ctx context.Context, id uuid.UUID, reason string) error
    GetUser(ctx context.Context, id uuid.UUID) (*identity.HumanUser, error)
    // ... other methods needed by initial workflows
}
```

Stub impl: returns `errNotImplemented`. gRPC impl lands in #15-revocation.

File: `plane/workflow/appclient/doc.go` documents ADR-019 routing rule.

## Step sequence — Phase D (canary)

### Step D1 — Canary workflow + activity

Files:

- `plane/workflow/canary/workflow.go`
- `plane/workflow/canary/activity.go`
- `plane/workflow/canary/bundle.go`

Canary reads `gitscale:dev:workflow:health` from `cache.Store` via a single read-only activity.

### Step D2 — Integration test

File: `plane/workflow/canary/integration_test.go`

- Spins Temporal dev server (testcontainers `temporalio/auto-setup` image OR `go.temporal.io/sdk/testsuite` time-skipping env).
- Spins Redis testcontainer.
- Sets the health key directly.
- Calls `client.ExecuteWorkflow(...)`; awaits result; asserts payload.

### Step D3 — Determinism lint negative test

File: `plane/workflow/lint/lint_test.go`

Runs `lint-determinism.sh` against `testdata/bad/`; asserts non-zero exit.

## Phase E — docs

### Step E1 — README

File: `plane/workflow/README.md`

Documents D1–D12 from spec, ADR-019 cross-link, plane-boundary rule, registry conventions, lint canon, retry-policy defaults, single-domain rule.

## Validation gates

| Gate | Command | Pass |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./plane/workflow/...` | exit 0 |
| Lint determinism | `make lint-determinism` | exit 0 on real code; exit 1 on `testdata/bad/` |
| Unit | `go test -race ./plane/workflow/...` (excludes integration tag) | pass |
| Canary integration | `go test -tags integration -race ./plane/workflow/canary/...` | pass |
| Worker boot | `docker-compose up -d temporal redis postgres && go run ./cmd/workflow-worker` | starts; canary workflow registered |
| Markdown | `make lint-md` | clean |

## Acceptance checklist

- [ ] `cmd/workflow-worker` builds; binary boots against docker-compose Temporal.
- [ ] Worker reads `TEMPORAL_NAMESPACE` from env, fails fast if unset.
- [ ] `Bundle` registry compiles; `canary.Bundle()` registers cleanly.
- [ ] OTel interceptor wired; canary spans show parent-child linkage.
- [ ] Worker options pinned per spec D8; env override works.
- [ ] `DefaultRetryPolicy` and `ShouldContinueAsNew` exported.
- [ ] Determinism lint fires on bad fixtures + clean on good.
- [ ] Canary integration test passes against real Redis + Temporal dev.
- [ ] `appclient.IdentityClient` interface exported; stub impl returns sentinel.
- [ ] No `plane/workflow/adapters/data/` package created.
- [ ] `plane/workflow/README.md` documents D1–D12.
- [ ] SIGTERM gracefully stops worker within `WORKER_STOP_TIMEOUT`.
- [ ] PR description cross-links ADR-019 + spec + execution plan.

## Risks

| Risk | Mitigation |
|---|---|
| Temporal dev server flaky in CI | restrict integration test to canary; unit tests use `testsuite.WorkflowTestSuite` time-skipping |
| Determinism rules too aggressive (false-positives in activity files) | activity files exempt by name pattern; `*_test.go` exempt |
| OTel collector misconfigured silently | worker logs OTLP target at startup; canary asserts span count > 0 |
| Adapter sprawl post-merge (engineers reach for `adapters/data/`) | README + skill hook + ADR-019 reference |
| `appclient.IdentityClient` stub merged but no impl follow-up | PR description explicitly cross-links #15-revocation as the impl source |
| Schedule API not yet supported in Temporal SDK version chosen | SDK ≥ v1.25.0 has Schedule support; pin version in go.mod |

## Rollback

`cmd/workflow-worker` not in any deployment yet; binary can be reverted independently. Determinism CI step can be disabled via `.github/workflows/go.yml` revert without breaking other gates.
