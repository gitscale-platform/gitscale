# Spec — Issue #110 CI cold pool — Firecracker microVM integration + agent-default routing

Date: 2026-05-09
Issue: https://github.com/gitscale-platform/gitscale/issues/110
Plane: workflow
Priority: p2 (Phase 2)
ADR-impact: conforming (ADR-002 Firecracker isolation; ADR-003 Temporal orchestration; ADR-008 outbox; ADR-019 workflow→application RPC boundary; ADR-010 SPIFFE; ADR-015 plan-approval)

## Problem

Phase 2 must ship a CI runner backed by **Firecracker microVMs** that is the
default execution surface for agent-submitted CI jobs. The architecture has
fixed every load-bearing knob — Firecracker (not Docker, not gVisor) per
ADR-002, two-tier pool (hot warm / cold scale-to-zero) per `architecture.md
§2.4`, agent-default cold pool per `architecture.md §6` — but no code yet
materializes a microVM, attaches a job to it, or charges its consumed minutes
to a billing account. The hot-pool stub from #33 (workflow bootstrap) reserves
the `QueueCIPipelines` task queue but registers no workflows or activities.

Until this lands, agent-class CI traffic has no execution surface and the
"agent default = cold pool" routing rule from `architecture.md §2.4` cannot
be honoured. REST API (#111) is merged and provides the principal-resolution
plumbing the trigger path will reuse.

## Goals

1. New package `plane/workflow/runner/` owning Firecracker microVM lifecycle
   as Temporal **activities only** (boot, attach, run, teardown). No
   workflow-scope I/O. No `time.*`, `os.*`, `net/*`, `math/rand` inside any
   workflow function.
2. New package `plane/workflow/ci/` owning the `CIJobWorkflow` and the
   `RunnerAssignment` deterministic routing decision. One workflow per CI
   job; idempotent on `CIJobID`.
3. Pool tier as a closed enum `Tier` with two values `TierHot` /
   `TierCold`. Routing rule: `principal.Kind() == Agent && annotation !=
   require-hot-pool` → `TierCold`; everything else → `TierHot`. Annotation
   override is debited from the org's hot-pool quota.
4. Cold-pool provisioning is on-demand: `BootColdVMActivity` allocates from
   a Firecracker host pool, returns a `MicroVMHandle{ID, VsockCID,
   IPv4, KernelImage, RootfsSnapshot}`. Hot-pool path calls
   `LeaseHotVMActivity` against the pre-warm fleet (returns same handle
   shape). Both activities have explicit `StartToCloseTimeout` and a
   bounded retry policy.
5. `RunJobActivity` issues the job command set over vsock to the in-VM
   agent, streams stdout/stderr to log sink (object store), returns
   `JobResult{ExitCode, DurationMS, BytesIngressed, BytesEgressed,
   PeakMemoryKB}`.
6. `TeardownVMActivity` is **idempotent on `MicroVMHandle.ID`**: if the VM
   is already gone (returns `ErrAlreadyTorndown`), the activity returns
   success. Workflow always invokes teardown via `defer` semantics
   (workflow-level `Selector` + cleanup branch on cancellation/timeout).
7. Resource limits at boot are sourced from the agent's billing account
   quota account: `vCPU`, `MemoryMB`, `EgressKB`, `WallClockSeconds`. Boot
   activity rejects (returns `ErrQuotaInsufficient`) if the requested
   shape exceeds the account's per-job ceiling.
8. Every CI job invocation emits a `ci.job_completed` outbox row via the
   application plane (`plane/workflow/appclient/billing.EmitUsageEvent`)
   carrying `principal_id`, `principal_kind`, `org_id`, `repo_id`,
   `tier`, `vcpu_seconds`, `memory_mb_seconds`, `egress_kb`. Workflows
   never publish to Kafka directly (ADR-008).
9. Boot/teardown failures route through compensation activities:
   `forceTeardownByID` is the saga compensation for `BootColdVMActivity`
   failure mid-handshake.
10. Integration test boots a Firecracker VM via the project's
    `pkg/microvm` wrapper (existing) or a stand-in `microvmtest.Fake`
    that satisfies the same interface; the fake is the unit-test path,
    the real wrapper is the gated integration path (build tag
    `firecracker_integration`).

## Non-goals

- Hot-pool sizing controller / pre-warm policy — `LeaseHotVMActivity`
  consumes a fleet manager that is out of scope for this PR. Stub
  fleet-manager interface lives in this PR; controller is a follow-up.
- Multi-step pipelines (matrix builds, fan-out, artifact passthrough) —
  this issue ships a single-job workflow. Pipeline composition is a
  follow-up that wraps `CIJobWorkflow` as a child workflow.
- Egress allowlist DSL — the boot activity accepts an allowlist slice
  but the policy resolution and DSL parser are #115's neighbour, not
  this PR. Allowlist for v1 = `[]string{"github.com:443", "<org-git-proxy>:443"}`.
- Cold-pool autoscaler — the activity allocates from whatever the
  fleet-manager interface returns; capacity is the cluster operator's
  problem, not the workflow's.
- AGENTS.md surfacing inside the VM — covered by #114.
- Billing aggregation / rollup — `ci.job_completed` is the raw event
  per-job; rollup is `plane/application/billing` territory.
- PR scoring / dedup — the workflow returns `JobResult`; downstream
  pipelines (`PRScoringWorkflow`) are unchanged.
- Cross-region failover of in-flight jobs. Job loss on host-pool node
  failure surfaces as `ErrVMLost`; the workflow returns failure rather
  than reattempting on a new host (CI jobs are not idempotent).

## Design decisions (defaults selected by supervisor)

| Question | Choice | Rationale |
|---|---|---|
| Sandbox technology | **Firecracker microVMs only** via `firecracker-go-sdk` (or the project's `pkg/microvm` wrapper if present). | ADR-002. Hardware boundary against untrusted agent code; Docker / gVisor / runc are forbidden (`gitscale-firecracker-isolation`). |
| Routing decision location | Inside `CIJobWorkflow` as a deterministic pure function `assignTier(principalKind, annotations)`. No activity, no I/O. | Determinism (ADR-003 / `gitscale-temporal-determinism`). The decision is a pure function of inputs already on the workflow input struct. |
| Tier enum | `type Tier int` closed enum `{ TierHot, TierCold }`; exhaustive switch enforced by `internal/architecture/` lint. | Closed sum type; new tier requires explicit code review per ADR-002 ("hardware boundary against untrusted code"). |
| Idempotency key for teardown | `MicroVMHandle.ID` — the Firecracker socket path / vsock CID. Activity catches `ErrNotFound` and returns success. | Temporal retries activities; double-teardown must be safe. Listed as explicit acceptance criterion on the issue. |
| Quota source | `billing.AgentQuotaAccount` resolved via `appclient.BillingClient.GetQuotaAccount(principalID)` at boot-activity entry. | ADR-019: workflow plane reads via app-plane client; never touches `plane/data/store` directly for state read with invariants. (Read-only is permitted, but billing's quota math has invariants — keep it in the app plane.) |
| Billing emission | `appclient.BillingClient.EmitUsageEvent` activity at workflow end (success or failure) — failed jobs still consume vcpu_seconds. Outbox row written in app-plane Tx (ADR-008). | Charging on failure mirrors cloud-provider norms; a runaway agent that crashes the VM still burns compute. Emission via app plane preserves the single-writer-per-aggregate rule (ADR-019). |
| Hot vs cold activity split | Two activities (`LeaseHotVMActivity`, `BootColdVMActivity`) returning the same `MicroVMHandle` shape; workflow calls one based on `tier`. | Clean retry-policy + timeout split: cold can take 30 s, hot must lease in < 1 s. Different `StartToCloseTimeout`. |
| Compensation pattern | `workflow.NewSelector` with a teardown branch on `Done()` / cancellation. Saga compensation array for `BootColdVMActivity` partial-success (VM allocated but RunJob never started). | Standard Temporal saga shape; no roll-your-own state machine. |
| Test isolation | `microvmtest.Fake` implements the `MicroVMProvisioner` interface for unit/workflow-suite tests. Real `firecracker-go-sdk` integration test gated by `//go:build firecracker_integration`. | Unit tests stay deterministic and CI-runnable on hosts without `/dev/kvm`. |
| Resource limit enforcement | Boot activity passes `vCPU`, `MemoryMB`, `--rlimit` analogues into the Firecracker config. Egress cap enforced via TC (traffic control) inside the VM template — out of scope to *configure* TC; the boot activity *passes the value*. | Defense-in-depth: workflow trusts the host VM template to honour the cap; quota check is the second layer. |

## Architecture

### Package layout

```
plane/workflow/runner/
  doc.go                    package doc, ADR-002 + ADR-003 refs, plane boundary
  microvm.go                MicroVMHandle, MicroVMProvisioner interface
  microvm_firecracker.go    firecracker-go-sdk provisioner (build tag)
  microvm_test.go           Fake provisioner used by ci/ workflow tests
  boot_cold_activity.go     BootColdVMActivity
  boot_cold_activity_test.go
  lease_hot_activity.go     LeaseHotVMActivity (stub fleet client)
  lease_hot_activity_test.go
  run_job_activity.go       RunJobActivity (vsock command + log streaming)
  run_job_activity_test.go
  teardown_activity.go      TeardownVMActivity (idempotent)
  teardown_activity_test.go
  bundle.go                 plane/workflow.Bundle for QueueCIPipelines
plane/workflow/ci/
  doc.go                    package doc, CIJobInput shape
  workflow.go               CIJobWorkflow, RunnerAssignment, assignTier
  workflow_test.go          temporal testsuite + Fake provisioner
  saga.go                   compensation helpers (forceTeardownByID)
  bundle.go                 plane/workflow.Bundle for QueueCIPipelines
plane/workflow/appclient/
  billing.go                (existing) — add EmitUsageEvent if absent
  billing_grpc.go           generated client wrapper if missing
proto/billing/v1/
  billing.proto             (existing) — add UsageEvent message + RPC if missing
plane/data/schema/migrations/
  NNNN_ci_outbox_event_kinds.sql  add 'ci.job_completed' to allowed event kind enum
```

### Workflow shape

```go
// plane/workflow/ci/workflow.go
package ci

import (
    "go.temporal.io/sdk/workflow"

    "github.com/gitscale-platform/gitscale/plane/workflow/runner"
)

type CIJobInput struct {
    JobID         uuid.UUID
    PrincipalID   uuid.UUID
    PrincipalKind PrincipalKind     // Human|Agent|Service
    OrgID         uuid.UUID
    RepoID        uuid.UUID
    Annotations   map[string]string // sorted-keys map; iterated only via deterministic helper
    Command       []string
    Env           map[string]string
    Resource      runner.ResourceShape // vCPU, MemoryMB, EgressKB, WallClockSeconds
}

type CIJobOutput struct {
    Tier      Tier
    VMID      string
    ExitCode  int
    DurationMS int64
    Result    runner.JobResult
}

func CIJobWorkflow(ctx workflow.Context, in CIJobInput) (CIJobOutput, error) {
    tier := assignTier(in.PrincipalKind, in.Annotations) // pure function

    // boot
    var handle runner.MicroVMHandle
    var bootErr error
    bootCtx := workflow.WithActivityOptions(ctx, bootOptionsFor(tier))
    if tier == TierCold {
        bootErr = workflow.ExecuteActivity(bootCtx, runner.BootColdVMActivity, runner.BootInput{
            JobID: in.JobID, Resource: in.Resource, PrincipalID: in.PrincipalID,
        }).Get(ctx, &handle)
    } else {
        bootErr = workflow.ExecuteActivity(bootCtx, runner.LeaseHotVMActivity, runner.LeaseInput{
            JobID: in.JobID, Resource: in.Resource,
        }).Get(ctx, &handle)
    }
    if bootErr != nil {
        return CIJobOutput{}, bootErr
    }

    // teardown is the saga compensation; ensure it always runs
    defer func() {
        // workflow `defer` semantics — executed via disconnected ctx so
        // teardown runs even on cancellation. Idempotent on handle.ID.
        dctx, _ := workflow.NewDisconnectedContext(ctx)
        _ = workflow.ExecuteActivity(
            workflow.WithActivityOptions(dctx, teardownOptions()),
            runner.TeardownVMActivity, handle.ID,
        ).Get(dctx, nil)
    }()

    // run
    var result runner.JobResult
    runCtx := workflow.WithActivityOptions(ctx, runOptions(in.Resource.WallClockSeconds))
    if err := workflow.ExecuteActivity(runCtx, runner.RunJobActivity, runner.RunInput{
        VMID: handle.ID, Command: in.Command, Env: in.Env,
    }).Get(ctx, &result); err != nil {
        return CIJobOutput{Tier: tier, VMID: handle.ID}, err
    }

    // billing — always emit, success or fail (compensating teardown on
    // failure is above; emission stays in the happy path here)
    emitCtx := workflow.WithActivityOptions(ctx, emitOptions())
    if err := workflow.ExecuteActivity(emitCtx, runner.EmitUsageEventActivity, runner.UsageInput{
        PrincipalID: in.PrincipalID, OrgID: in.OrgID, RepoID: in.RepoID,
        Tier: tier.String(), Result: result,
    }).Get(ctx, nil); err != nil {
        // emission failure is non-fatal to the job result but is logged
        // by the activity itself; the activity is retried per its policy.
        return CIJobOutput{Tier: tier, VMID: handle.ID, Result: result}, err
    }

    return CIJobOutput{Tier: tier, VMID: handle.ID, Result: result, ExitCode: result.ExitCode}, nil
}

// assignTier — pure deterministic function. No I/O, no time, no random.
func assignTier(kind PrincipalKind, ann map[string]string) Tier {
    if ann["require-hot-pool"] == "true" {
        return TierHot
    }
    if kind == PrincipalAgent {
        return TierCold
    }
    return TierHot
}
```

Determinism notes (audit trail for `gitscale-temporal-determinism`):

- Map `Annotations` is read by **explicit key**, never iterated. Any
  future predicate that needs to walk all annotation keys must wrap the
  map with `sortedKeys(ann)` (provided in `ci/workflow.go`).
- No `time.*` in workflow body. All timeouts come from
  `workflow.WithActivityOptions(...)`. `bootOptionsFor(TierCold)` sets
  `StartToCloseTimeout: 60 * time.Second`; `bootOptionsFor(TierHot)`
  sets `StartToCloseTimeout: 5 * time.Second`.
- No `go func()`, no `chan`, no `sync.*`. Saga shape uses
  `workflow.NewSelector` only if a future revision adds parallel
  steps; the v1 shape is fully sequential.
- Teardown uses `workflow.NewDisconnectedContext` so it survives
  workflow cancellation.

### `MicroVMProvisioner` interface

```go
// plane/workflow/runner/microvm.go
package runner

type ResourceShape struct {
    VCPU              int
    MemoryMB          int
    EgressKB          int64
    WallClockSeconds  int
    EgressAllowlist   []string
}

type MicroVMHandle struct {
    ID            string // unique; matches Firecracker socket path basename
    VsockCID      uint32
    IPv4          string
    KernelImage   string
    RootfsSnapshot string
}

type JobResult struct {
    ExitCode        int
    DurationMS      int64
    BytesIngressed  int64
    BytesEgressed   int64
    PeakMemoryKB    int64
    LogsObjectURI   string
}

type MicroVMProvisioner interface {
    BootCold(ctx context.Context, in BootInput) (MicroVMHandle, error)
    LeaseHot(ctx context.Context, in LeaseInput) (MicroVMHandle, error)
    Run(ctx context.Context, vmID string, in RunInput) (JobResult, error)
    Teardown(ctx context.Context, vmID string) error // idempotent
}
```

Real impl (`microvm_firecracker.go`, build tag `firecracker_integration`)
wraps `firecracker-go-sdk`. Test impl (`microvmtest.Fake`) records calls
and returns scripted handles.

### Activity timeouts (explicit per `gitscale-temporal-determinism`)

| Activity | StartToClose | RetryPolicy |
|---|---|---|
| `BootColdVMActivity` | 60 s | 3 attempts, 1 s initial, 2× backoff, non-retryable: `ErrQuotaInsufficient` |
| `LeaseHotVMActivity` | 5 s | 5 attempts, 200 ms initial, 1.5× backoff |
| `RunJobActivity` | `WallClockSeconds + 60 s` | 1 attempt (non-retryable; CI jobs have side effects) |
| `TeardownVMActivity` | 30 s | 5 attempts, 1 s initial, 2× backoff (idempotent → safe to retry) |
| `EmitUsageEventActivity` | 10 s | 10 attempts, 1 s initial, 2× backoff |

Defaults must not be implicit anywhere — `gitscale-temporal-determinism`
forbids `worker.Options.DefaultActivityOptions` for these.

### Plane boundary (ADR-019)

| Caller | Callee | Allowed? |
|---|---|---|
| `plane/workflow/runner/*` | `firecracker-go-sdk`, `pkg/microvm` | yes (activity scope; hardware-isolation boundary) |
| `plane/workflow/runner/EmitUsageEventActivity` | `appclient.BillingClient.EmitUsageEvent` | yes; app plane writes outbox in same Tx (ADR-008/019) |
| `plane/workflow/runner/BootColdVMActivity` | `appclient.BillingClient.GetQuotaAccount` | yes (read with invariant; routes via app plane) |
| `plane/workflow/ci/*` | `plane/data/store.MetadataStore` | **no** (lint blocks; ADR-019) |
| `plane/workflow/runner/*` | `github.com/docker/*`, `github.com/containerd/*`, `github.com/google/gvisor/*`, `runc`, `runsc`, `podman`, `nerdctl` | **no** (ADR-002; lint blocks; `gitscale-firecracker-isolation`) |

### Outbox event

```sql
-- plane/data/schema/migrations/NNNN_ci_outbox_event_kinds.sql
-- Extend the allowed event_kind enum used by the billing outbox table to
-- include the CI domain's job-completion event. The event payload schema
-- lives in plane/application/billing/events.go (Go-side).

ALTER TYPE billing.outbox_event_kind ADD VALUE IF NOT EXISTS 'ci.job_completed';
```

Payload shape (`plane/application/billing/events.go`):

```go
type CIJobCompletedEvent struct {
    EventID        uuid.UUID `json:"event_id"`
    JobID          uuid.UUID `json:"job_id"`
    PrincipalID    uuid.UUID `json:"principal_id"`
    PrincipalKind  string    `json:"principal_kind"`  // human|agent|service
    OrgID          uuid.UUID `json:"org_id"`
    RepoID         uuid.UUID `json:"repo_id"`
    Tier           string    `json:"tier"`            // hot|cold
    VCPUSeconds    float64   `json:"vcpu_seconds"`
    MemoryMBSeconds float64  `json:"memory_mb_seconds"`
    EgressKB       int64     `json:"egress_kb"`
    ExitCode       int       `json:"exit_code"`
    OccurredAt     time.Time `json:"occurred_at"`
}
```

Stamped fields per ADR-019 audit context (`actor_kind=service`,
`actor_id=<workflow-worker SPIFFE id>`) added by the app-plane side of
`EmitUsageEvent`.

### Quota / metering hop (`gitscale-agent-quota-check`)

CI job invocations are agent-class traffic by default. The two-step
admission rule:

1. **Identity already verified** at the trigger entry-point (REST
   `/v1/repos/{id}/ci/jobs` from #111 — separate PR; this issue ships
   the workflow, not the trigger HTTP route). The workflow input
   carries `PrincipalID` + `PrincipalKind` resolved by the existing
   `Principal` middleware.
2. **Quota deducted at boot**: `BootColdVMActivity` calls
   `BillingClient.GetQuotaAccount(principalID)` and rejects with
   `ErrQuotaInsufficient` when the requested `ResourceShape` exceeds
   per-job ceiling. Hot-pool path same check.
3. **Counter incremented at exit**: `EmitUsageEventActivity` writes the
   `ci.job_completed` outbox row; the billing roll-up consumer (#11)
   debits the account on event consumption (downstream; not this PR).

The workflow itself does not touch Redis rate-limit keys — those are
the edge plane's responsibility for the trigger-HTTP path (out of
scope).

### Failure modes

| Failure | Behaviour |
|---|---|
| Boot fails before VM allocated | Activity returns; workflow returns failure; no teardown needed (no handle) |
| Boot allocates VM, then RunJob fails | `defer` teardown runs; usage event still emitted with `ExitCode = -1` and observed resource consumption |
| RunJob succeeds, EmitUsage fails | Activity retries per policy; on permanent fail, workflow returns the emission error — operator alarm; the VM is already torn down so no resource leak |
| Workflow cancellation mid-RunJob | Disconnected-context teardown runs; emission attempted with `ExitCode = -2 ` (cancelled) |
| Teardown fails permanently | Returns last error after retries; alarm. Operator-side reaper sweeps orphan VMs by `JobID` label nightly (out of scope; tracked as follow-up) |
| Host loss during RunJob | `ErrVMLost`; workflow fails, teardown is no-op (host gone), emission attempted; orphan host cleanup is reaper territory |

## Testing strategy

1. **Pure-function unit test** — `assignTier(kind, annotations)` truth
   table: 6 rows × expected `Tier`. No imports beyond `testing`.
2. **Activity unit tests** — each activity in isolation against
   `microvmtest.Fake`; assert (a) explicit timeouts honoured, (b)
   non-retryable error classes correctly classified, (c) teardown is
   idempotent (call twice → success twice).
3. **Workflow test** — `temporal/sdk/testsuite.WorkflowTestSuite`
   exercising `CIJobWorkflow` end-to-end with `Fake` provisioner.
   Scenarios: agent → cold path; human → hot path; agent +
   `require-hot-pool` annotation → hot path; boot fail → no teardown
   (handle never produced); run fail → teardown ran; cancellation →
   disconnected teardown ran.
4. **Determinism replay test** — feed the same input twice, assert
   identical history. Reject any drift (uses Temporal's
   `ReplayWorkflowHistory`).
5. **Integration test** (build tag `firecracker_integration`) — boots
   a real Firecracker VM via `pkg/microvm`, runs `/bin/true`, asserts
   exit 0 and clean teardown. Skipped on hosts without `/dev/kvm`; CI
   gates this on a self-hosted runner.
6. **Plane-boundary lint test** — assert `plane/workflow/ci/` does not
   import `plane/data/store`; assert `plane/workflow/runner/` does not
   import `docker`, `containerd`, `runc`, `runsc`, `podman`,
   `gvisor`. Negative tests temporarily add the import and confirm
   lint fails.
7. **Emission integration** — workflow test with stub
   `BillingClient` records the `EmitUsageEvent` call; assert all 10
   fields populated; asserts emission ran on both success and failure
   paths.

## Risks / unknowns

- **`firecracker-go-sdk` vs `pkg/microvm` wrapper**: project may already
  have a `pkg/microvm` wrapper from #33; if absent, this PR introduces
  the SDK direct. Either way the import surface stays inside
  `plane/workflow/runner/microvm_firecracker.go` and is gated by build
  tag.
- **Hot-pool fleet manager**: `LeaseHotVMActivity` depends on a
  fleet-manager interface (`FleetManager.Lease(ctx, shape) (vmID,
  error)`) that this PR introduces as an interface but stubs as a
  fixed-pool in-memory implementation. Real implementation is a
  follow-up issue (sizing controller, drain, refill). Cold-pool path
  is the agent default and lands fully here.
- **Billing event kind enum**: the migration assumes the
  `billing.outbox_event_kind` Postgres ENUM exists. If the project
  uses a `CHECK` constraint instead, the migration adapts; either way
  the Go-side `events.go` constant is the source of truth.
- **`ResourceShape` defaults**: input may omit fields; activity
  resolves defaults from a Go-side constant block, not from env.
  Workflow input is the only configuration surface.
- **ADR mismatch on the issue**: issue body cites "ADR-016 (CI
  isolation: Firecracker)"; the canonical ADR for Firecracker per
  `docs/architecture.md §8` is **ADR-002**. ADR-016 is Vespa/search.
  Spec cites ADR-002. Flag for `adr-historian` at PR review time.
- **Egress allowlist**: v1 hardcodes a two-entry list. The DSL
  surfaces with #115 (plan-approval) and the AGENTS.md surface with
  #114; cross-issue coordination is out of scope here but the
  `EgressAllowlist []string` field is on the input shape so the
  follow-up PR is additive.

## Plane-boundary summary

- `plane/workflow/runner/` and `plane/workflow/ci/` may import each
  other and `plane/workflow/appclient/`.
- Neither may import `plane/data/store`, `plane/application/...`, or
  any Docker/gVisor/runc client. `internal/architecture/` lint
  enforces both rules; PR adds tests.
- Outbox writes happen on the app-plane side of
  `appclient.BillingClient.EmitUsageEvent` — workflow plane never
  produces to Kafka and never opens a DB Tx.
