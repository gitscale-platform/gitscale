# Plan — Issue #110 CI cold pool — Firecracker microVM integration + agent-default routing

- Spec: `docs/superpowers/specs/2026-05-09-issue-110-ci-firecracker-design.md`
- Issue: #110
- Branch: `feat/workflow-ci-firecracker-cold-pool`
- Subagent: `workflow-plane`
- Mandatory pre-commit skills: `gitscale-firecracker-isolation`, `gitscale-temporal-determinism`, `gitscale-adr-guard`, `gitscale-plane-boundary`, `gitscale-go-conventions`, `gitscale-agent-quota-check`
- ADR review at PR-time: `comprehensive-review:architect-review` (binds ADR-002, ADR-003, ADR-008, ADR-019; introduces a new outbox event kind)
- Predecessors: #111 (REST API — merged; principal-resolution surface reused), #33 (workflow bootstrap — `QueueCIPipelines` and `Bundle` scaffolding)

## Tasks (commit boundaries)

### Task 1 — `runner` package skeleton + types (commit 1)

- Create `plane/workflow/runner/doc.go` citing ADR-002 + ADR-003 + ADR-019 in package doc; explicit "Firecracker is the only sandbox; Docker/gVisor/runc are forbidden in this package."
- Create `plane/workflow/runner/microvm.go` with `ResourceShape`, `MicroVMHandle`, `JobResult`, `BootInput`, `LeaseInput`, `RunInput`, `UsageInput`, `MicroVMProvisioner` interface (per spec §"`MicroVMProvisioner` interface").
- Create `plane/workflow/runner/microvmtest/fake.go` exporting a `Fake` provisioner that records calls and returns scripted handles. Used by every workflow / activity test downstream.
- No Firecracker import yet. No activities yet.

**DoD**: `go vet ./plane/workflow/runner/...` clean; `go build ./plane/workflow/runner/...` clean; `gitscale-go-conventions` clean; `gitscale-firecracker-isolation` clean (no forbidden imports — only types).

### Task 2 — Pure routing function + `ci` package skeleton (commit 2)

- Create `plane/workflow/ci/doc.go` citing ADR-002 + ADR-003 + the determinism contract.
- Create `plane/workflow/ci/workflow.go` with:
  - `CIJobInput`, `CIJobOutput`, `Tier` closed enum, `PrincipalKind` closed enum.
  - `assignTier(kind, ann) Tier` — pure function per spec §Determinism.
  - `sortedKeys(m map[string]string) []string` helper for any future deterministic map iteration.
- Create `plane/workflow/ci/workflow_test.go` with the `assignTier` truth-table test (6 rows) — no Temporal SDK import needed for this test.

**DoD**: `go test ./plane/workflow/ci/...` passes; `gitscale-temporal-determinism` clean (no forbidden symbols); `assignTier` has 100% branch coverage.

### Task 3 — `BootColdVMActivity` + `LeaseHotVMActivity` (commit 3)

- Create `plane/workflow/runner/boot_cold_activity.go`:
  - Constructor takes `MicroVMProvisioner` + `appclient.BillingClient`.
  - Activity body: `GetQuotaAccount` → reject `ErrQuotaInsufficient` if shape exceeds ceiling → `BootCold` on provisioner → return handle.
  - Non-retryable errors enumerated (`ErrQuotaInsufficient`, `ErrInvalidShape`); transport errors retryable.
- Create `plane/workflow/runner/lease_hot_activity.go` mirror against `BootCold` with `LeaseHot`.
- Tests against `microvmtest.Fake` + a `stubBillingClient`: quota-ok happy path; quota-exceeded → non-retryable error; provisioner failure → retryable error class.

**DoD**: `go test -race ./plane/workflow/runner/...` green; activity error classification asserted in tests; `gitscale-firecracker-isolation` clean.

### Task 4 — `RunJobActivity` + `TeardownVMActivity` (commit 4)

- Create `plane/workflow/runner/run_job_activity.go` calling `provisioner.Run(ctx, vmID, in)`. Single-attempt non-retryable per spec §"Activity timeouts".
- Create `plane/workflow/runner/teardown_activity.go` calling `provisioner.Teardown(ctx, vmID)`. Catch `ErrAlreadyTorndown` / `ErrNotFound` and return success — idempotent.
- Tests:
  - `RunJob` happy path returns `JobResult` shape.
  - `Teardown` called twice — both return success.
  - `Teardown` of unknown ID — success.
  - `Teardown` underlying transport failure — error returned.

**DoD**: idempotency unit-test asserted; `gitscale-firecracker-isolation` clean.

### Task 5 — `EmitUsageEventActivity` + `appclient.BillingClient.EmitUsageEvent` (commit 5)

- If `appclient.BillingClient` already exposes `EmitUsageEvent`, skip. Else extend `proto/billing/v1/billing.proto` with:

```proto
message UsageEvent {
  bytes event_id = 1;       // uuid
  bytes job_id = 2;
  bytes principal_id = 3;
  string principal_kind = 4;
  bytes org_id = 5;
  bytes repo_id = 6;
  string tier = 7;
  double vcpu_seconds = 8;
  double memory_mb_seconds = 9;
  int64 egress_kb = 10;
  int32 exit_code = 11;
  google.protobuf.Timestamp occurred_at = 12;
}

service BillingService {
  rpc EmitUsageEvent(UsageEvent) returns (UsageEventAck);
}
```

- Regenerate stubs (`buf generate` per repo norm).
- App-plane impl in `plane/application/billing/grpc_server.go`: writes the source row + outbox row in one Tx via `MetadataStore.Transact` (ADR-008). Stamp `actor_kind=service`, `actor_id=<workflow-worker SPIFFE ID from ctx>` (ADR-019).
- Workflow-plane wrapper in `plane/workflow/appclient/billing.go` if absent.
- New activity `plane/workflow/runner/emit_usage_event_activity.go` calls the wrapper.
- Tests: app-plane integration test against PG testcontainer asserts source + outbox rows in one Tx; workflow-plane test asserts the activity calls the client and returns its error.

**DoD**: `make lint-events` clean (outbox parity); ADR-008 contract preserved; `gitscale-plane-boundary` clean.

### Task 6 — Migration: `ci.job_completed` event kind (commit 6)

- Create `plane/data/schema/migrations/NNNN_ci_outbox_event_kinds.sql`:

```sql
ALTER TYPE billing.outbox_event_kind ADD VALUE IF NOT EXISTS 'ci.job_completed';
```

- If the schema uses a `CHECK` constraint instead of an ENUM (inspect `plane/data/schema/migrations/`), adapt to extend the constraint.
- Add Go-side constant `EventKindCIJobCompleted = "ci.job_completed"` in `plane/application/billing/events.go`.
- Compliance: extend the existing `events_test.go` allowlist to include the new kind.

**DoD**: migration applies forward and is idempotent on re-apply (test in PG testcontainer); existing event-kind tests still pass.

### Task 7 — `CIJobWorkflow` (commit 7)

- Flesh out `plane/workflow/ci/workflow.go` with the full workflow body per spec §"Workflow shape":
  - `assignTier`
  - `BootCold` or `LeaseHot` per tier with explicit `ActivityOptions` per spec table.
  - `defer` teardown via `workflow.NewDisconnectedContext`.
  - `RunJobActivity`.
  - `EmitUsageEventActivity` on success and on failure paths (success of teardown is decoupled from emission failure-mode handling — emission alarm is non-fatal but surfaces via activity retry).
- Add `plane/workflow/ci/saga.go` with `forceTeardownByID(ctx, vmID)` helper used by both happy and unhappy paths.

**DoD**: `gitscale-temporal-determinism` clean; no `time.*`, `os.*`, `net/*`, `math/rand`, `go func`, `sync.*` in `workflow.go`; map iteration only via `sortedKeys`.

### Task 8 — Workflow tests (commit 8)

- `plane/workflow/ci/workflow_test.go` with `temporal/sdk/testsuite.WorkflowTestSuite`:
  - `TestCIJob_AgentDefaultsToColdPool`
  - `TestCIJob_HumanDefaultsToHotPool`
  - `TestCIJob_AgentRequireHotPoolAnnotation_RoutesHot`
  - `TestCIJob_BootFailure_NoTeardownAttempted`
  - `TestCIJob_RunFailure_TeardownRuns_EmissionWithExitCodeMinusOne`
  - `TestCIJob_Cancellation_DisconnectedTeardownRuns`
  - `TestCIJob_QuotaExceeded_NonRetryable`
- Determinism replay test `TestCIJob_DeterministicReplay` using `worker.NewWorkflowReplayer().ReplayWorkflowHistory`.

**DoD**: `go test -race ./plane/workflow/ci/...` green; replay test green; tests use `microvmtest.Fake`, no Firecracker import.

### Task 9 — Bundles + `cmd/workflow-worker` registration (commit 9)

- Create `plane/workflow/runner/bundle.go` exporting `Bundle()` that returns the `plane/workflow.Bundle` for `QueueCIPipelines` with all six activities (Boot/Lease/Run/Teardown/Emit + force teardown).
- Create `plane/workflow/ci/bundle.go` exporting `Bundle()` that registers `CIJobWorkflow` on `QueueCIPipelines`.
- Wire both into `cmd/workflow-worker/main.go` collecting bundles; do not modify the worker entrypoint beyond appending two `Bundle()` calls (registry pattern from #33).

**DoD**: `cmd/workflow-worker` builds; running it locally registers the workflow + activities (verified via Temporal Web in dev compose, optional manual gate); `gitscale-plane-boundary` clean.

### Task 10 — Architecture lint coverage (commit 10)

- Extend `internal/architecture/` lint:
  - `plane/workflow/runner/` and `plane/workflow/ci/` must not import `plane/data/store`, `plane/application/`, `github.com/docker/`, `github.com/containerd/`, `github.com/opencontainers/runc`, `github.com/google/gvisor`, `github.com/containers/podman`.
  - `plane/workflow/ci/workflow.go` must not import `time`, `os`, `net`, `math/rand`, `sync` (workflow-determinism guard).
- Negative tests: temporarily add a forbidden import and assert the lint fails; revert.

**DoD**: lint passes on clean tree; lint fails on every negative test variant.

### Task 11 — Real Firecracker integration (commit 11, build-tagged)

- Create `plane/workflow/runner/microvm_firecracker.go` with build tag `//go:build firecracker_integration`. Implements `MicroVMProvisioner` against `firecracker-go-sdk` (or the `pkg/microvm` wrapper if present in the tree — inspect first).
- Create `plane/workflow/runner/microvm_firecracker_integration_test.go` with the same build tag: boots a real microVM, runs `/bin/true`, tears down, asserts exit 0 and no orphan socket.
- Integration test is **not** wired into default `go test ./...`; runs only on a self-hosted runner with `/dev/kvm`. Document in `plane/workflow/runner/doc.go`.

**DoD**: build tag isolates the SDK import; `go test ./...` (no tag) ignores this file; `go test -tags firecracker_integration ./plane/workflow/runner/...` passes on the self-hosted runner (operator gate, not a CI block for this PR).

## Pre-push gate

Run on the worktree before `gh pr create`:

```bash
go vet ./...
golangci-lint run
go test -race ./plane/workflow/... ./plane/application/billing/... ./internal/architecture/... -count=1
make lint-events
make lint-determinism
make lint-md
```

Expected: all green. The Firecracker integration test is **not** part of
this gate; it is operator-run on `/dev/kvm` hosts.

## Mandatory pre-PR skills (run sequentially, paste output into PR body)

1. `gitscale-firecracker-isolation` — verdict on `plane/workflow/runner/`
   and `plane/workflow/ci/`. Must be `ok`.
2. `gitscale-temporal-determinism` — scan `plane/workflow/ci/workflow.go`
   and every activity. Activities may use `time.*`; workflow body must not.
3. `gitscale-adr-guard` — verify ADR-002, ADR-003, ADR-008, ADR-019
   citations match changes; flag the issue-body's ADR-016 typo to
   `adr-historian` for archival note.
4. `gitscale-plane-boundary` — verify no `plane/data/store` import from
   `plane/workflow/ci/`; no `plane/application/` import from either
   workflow package.
5. `gitscale-agent-quota-check` — verify boot activities call
   `GetQuotaAccount`; verify emission activity runs on every termination
   path.
6. `gitscale-go-conventions`.

## Self-review battery (parallel, paste verdicts into PR body)

- `pr-review-toolkit:code-reviewer`
- `pr-review-toolkit:silent-failure-hunter` — teardown errors must be
  surfaced (logged + returned), not swallowed; emission errors must be
  retryable per policy, not silently dropped.
- `pr-review-toolkit:type-design-analyzer` — `Tier`, `PrincipalKind`,
  `ResourceShape`, `MicroVMHandle`, `JobResult` are the load-bearing
  shapes.
- `pr-review-toolkit:pr-test-analyzer` — confirm replay-determinism
  test is present and the cancellation path is covered.
- `adr-historian` — note ADR-002 binding; flag issue-body ADR-016
  reference as a typo (canonical = ADR-002).

## PR body template

```
gh pr create --title "[Workflow] CI cold pool — Firecracker microVM integration + agent-default routing" --body "$(cat <<'EOF'
## Summary

- New `plane/workflow/runner/` package: Firecracker-backed Temporal activities (Boot cold / Lease hot / Run / Teardown / EmitUsage). Real SDK gated behind `firecracker_integration` build tag; default tests use `microvmtest.Fake`.
- New `plane/workflow/ci/` package: `CIJobWorkflow` with deterministic `assignTier` routing — agents default to cold pool, humans to hot pool, `require-hot-pool` annotation overrides.
- New outbox event kind `ci.job_completed` emitted via `appclient.BillingClient.EmitUsageEvent` (ADR-008/019). Migration extends `billing.outbox_event_kind`.
- Architecture lint extended to enforce ADR-002 (no Docker/gVisor/runc/podman in the runner package) and ADR-003 (no `time`/`os`/`net`/`math/rand`/`sync` in workflow body).

## ADR-impact

- ADR-002 (Firecracker isolation): conforming. New code is the first concrete CI runner; lint enforces no container-runtime imports.
- ADR-003 (Temporal): conforming. Determinism replay test included.
- ADR-008 (outbox): conforming. Emission writes source + outbox in one Tx on the app-plane side.
- ADR-019 (workflow→app-plane RPC): conforming. Quota read + emission both routed via `appclient.BillingClient`.

Note: issue body cites "ADR-016 (CI isolation: Firecracker)"; canonical is ADR-002. Flagged for adr-historian.

## Test plan

- [x] `go test -race ./plane/workflow/runner/... ./plane/workflow/ci/... ./plane/application/billing/...`
- [x] `assignTier` truth-table (6 rows)
- [x] Workflow tests: agent→cold, human→hot, annotation override, boot fail, run fail, cancellation, quota exceeded
- [x] Determinism replay test (`ReplayWorkflowHistory`)
- [x] Migration apply/re-apply on PG testcontainer
- [x] Plane-boundary lint negative tests
- [ ] Firecracker integration test on self-hosted `/dev/kvm` runner (operator gate; out of band of this PR's CI)

Spec: docs/superpowers/specs/2026-05-09-issue-110-ci-firecracker-design.md
Plan: docs/superpowers/plans/2026-05-09-issue-110-ci-firecracker-plan.md

<details><summary>Self-review</summary>

- gitscale-firecracker-isolation: <verdict>
- gitscale-temporal-determinism: <verdict>
- gitscale-adr-guard: <verdict>
- gitscale-plane-boundary: <verdict>
- gitscale-agent-quota-check: <verdict>
- code-reviewer: <verdict>
- silent-failure-hunter: <verdict>
- type-design-analyzer: <verdict>
- pr-test-analyzer: <verdict>
- adr-historian: <verdict>

</details>

Closes #110.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

## Self-review (plan author)

**Spec coverage:**
- Firecracker microVM lifecycle (boot/run/teardown) — Tasks 3, 4, 11.
- Two-tier pool with agent-default cold — Tasks 2, 7.
- Idempotent teardown — Task 4 + assertion in workflow tests (Task 8).
- Resource limits from agent quota account — Task 3.
- Billing emission per-job — Task 5 + Task 7.
- Determinism guarantees — Tasks 2, 7, 10 + replay test (Task 8).
- Plane boundary enforcement — Task 10.

**Determinism scan:** `assignTier` (Task 2) is the only routing logic
inside the workflow body and is a pure function over already-resolved
inputs. All time / random / network calls live in activities. Map
iteration only via `sortedKeys` (Task 2). Task 10 lint forbids the
forbidden imports at the package level — failure mode is a build break,
not a runtime drift.

**Type consistency:** `MicroVMHandle.ID`, `Tier`, `ResourceShape`,
`JobResult` referenced consistently across spec and plan.

**Placeholder scan:** Task 11 directs the implementer to inspect for an
existing `pkg/microvm` wrapper before introducing
`firecracker-go-sdk` direct. Acceptable — wrapper presence depends on
whether #33 landed it; either path keeps the SDK import inside one
build-tagged file.

**ADR-mismatch flag:** issue body's "ADR-016" reference is recorded as a
typo to flag at PR review (canonical: ADR-002). Documented in spec
§"Risks / unknowns" and PR body.
