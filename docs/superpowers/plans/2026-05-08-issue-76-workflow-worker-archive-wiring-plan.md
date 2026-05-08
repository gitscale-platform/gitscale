# Issue #76 cmd/workflow-worker archive wiring — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Replace the `nil` ArchiveDeps in `cmd/workflow-worker/main.go` with real wiring (gRPC billing client, Vault key provider, S3 object store, postgres archiver). Add an `ArchiveRouterWorkflow` so the schedule's static-args limitation is bypassed; the router computes `(year,month) = workflow.Now − 18 months` and starts `PartitionArchiveWorkflow` as a child.

**Architecture:** All cmd-level glue + a small router workflow + wiring of `EnsureArchiveSchedule` to target the router. Activity registration through existing `billing.Bundle`. Heavy integration test gated by `//go:build integration`.

**Tech Stack:** Go 1.22, AWS SDK Go v2, hashicorp/vault/api, grpc-go, Temporal Go SDK.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-76-workflow-worker-archive-wiring-design.md`

**Branch:** `feat/workflow-worker-archive-wiring`

---

## File map

### Create
- `plane/workflow/billing/archive_router_workflow.go`
- `plane/workflow/billing/archive_router_workflow_test.go`
- `cmd/workflow-worker/integration_test.go` (or extend existing if present)

### Modify
- `cmd/workflow-worker/main.go` — build real ArchiveDeps; register router; call EnsureArchiveSchedule
- `plane/workflow/billing/archive_schedules.go` — target ArchiveRouterWorkflow; remove ATTENTION block
- `plane/workflow/billing/bundle.go` — register router alongside archive workflow

---

## Pre-flight

- [ ] **Step P.1: Worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
git worktree add -b feat/workflow-worker-archive-wiring \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-workflow-worker-archive-wiring \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-workflow-worker-archive-wiring
git status --porcelain
```

- [ ] **Step P.2: Baseline**

```bash
go build ./...
go test -race ./plane/workflow/... ./cmd/workflow-worker/... -count=1
```

---

## Task 1: ArchiveRouterWorkflow + tests

**Files:**
- `plane/workflow/billing/archive_router_workflow.go`
- `plane/workflow/billing/archive_router_workflow_test.go`

- [ ] **Step 1.1: archive_router_workflow.go**

```go
package billing

import (
	"fmt"

	"go.temporal.io/sdk/workflow"
)

// ArchiveRouterWorkflowName is the registered name. The schedule targets
// this router; the router computes (year, month) at fire time and spawns
// PartitionArchiveWorkflow as a child. This pattern works around
// ScheduleWorkflowAction's static-args limitation while preserving
// PartitionArchiveWorkflow determinism.
const ArchiveRouterWorkflowName = "billing.ArchiveRouter"

// ArchiveRouterWorkflow computes (year, month) := now − 18 months and starts
// PartitionArchiveWorkflow as a child workflow.
func ArchiveRouterWorkflow(ctx workflow.Context) error {
	now := workflow.Now(ctx)
	target := now.AddDate(0, -18, 0)
	year, month := target.Year(), int(target.Month())

	cwo := workflow.ChildWorkflowOptions{
		WorkflowID: fmt.Sprintf("billing-archive-%04d-%02d", year, month),
	}
	cctx := workflow.WithChildOptions(ctx, cwo)

	var result PartitionArchiveResult
	return workflow.ExecuteChildWorkflow(cctx, PartitionArchiveWorkflow, ArchiveInput{
		Year: year, Month: month,
	}).Get(ctx, &result)
}
```

If `PartitionArchiveResult` is named differently in the existing
`archive_workflow.go` (likely `ArchiveResult` or similar), use the canonical
name. Inspect `plane/workflow/billing/archive_workflow.go` first.

- [ ] **Step 1.2: archive_router_workflow_test.go**

Mirror the existing testsuite shape (e.g. `plane/workflow/billing/workflow_test.go`):

```go
package billing_test

import (
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/workflow/billing"
	"go.temporal.io/sdk/testsuite"
)

func TestArchiveRouter_Computes18MonthLag(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	// Fix workflow.Now to a known timestamp.
	env.SetStartTime(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))

	var startedYear, startedMonth int
	env.OnWorkflow("PartitionArchiveWorkflow", mock.Anything).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(billing.ArchiveInput)
			startedYear, startedMonth = in.Year, in.Month
		}).
		Return(billing.PartitionArchiveResult{}, nil).
		Once()

	env.RegisterWorkflow(billing.ArchiveRouterWorkflow)
	env.ExecuteWorkflow(billing.ArchiveRouterWorkflow)

	if !env.IsWorkflowCompleted() {
		t.Fatal("not completed")
	}
	if startedYear != 2024 || startedMonth != 11 {
		t.Fatalf("expected child for 2024-11, got %d-%d", startedYear, startedMonth)
	}
}
```

(`mock.Arguments` is from `github.com/stretchr/testify/mock`; the testsuite's
`OnWorkflow` may differ in your repo's SDK version — adapt to the canonical
mock pattern used by the existing archive_workflow_test.go.)

- [ ] **Step 1.3: Run**

```bash
go test -race -run TestArchiveRouter ./plane/workflow/billing/... -count=1
```

Expected: PASS.

- [ ] **Step 1.4: Commit**

```bash
git add plane/workflow/billing/archive_router_workflow.go \
        plane/workflow/billing/archive_router_workflow_test.go
git commit -m "$(cat <<'EOF'
feat(workflow): ArchiveRouterWorkflow for #76

Computes (year, month) = now − 18 months at fire time and spawns
PartitionArchiveWorkflow as a child. Workaround for Temporal's static
ScheduleWorkflowAction.Args; PartitionArchiveWorkflow determinism is
preserved.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Register router in bundle + schedule

**Files:**
- `plane/workflow/billing/bundle.go`
- `plane/workflow/billing/archive_schedules.go`

- [ ] **Step 2.1: bundle.go — register the router workflow**

Inspect `bundle.go::Bundle.Apply` (or equivalent). Append a `RegisterWorkflow(ArchiveRouterWorkflow)` call alongside the existing PartitionArchiveWorkflow registration.

If registration is by name (with options), use:

```go
RegisterWorkflowWithOptions(ArchiveRouterWorkflow, workflow.RegisterOptions{
    Name: ArchiveRouterWorkflowName,
})
```

- [ ] **Step 2.2: archive_schedules.go — target router**

Find the `ScheduleWorkflowAction.Workflow` field assignment. Change from
`PartitionArchiveWorkflow` (or whatever it currently targets) to
`ArchiveRouterWorkflow`. Remove `Args` if present (router takes none).

Remove the file-level ATTENTION block declaring "args wiring pending".

- [ ] **Step 2.3: Build + test**

```bash
go build ./plane/workflow/billing/...
go test -race ./plane/workflow/billing/... -count=1
```

Expected: PASS. Existing tests of `EnsureArchiveSchedule` may need updating
to match the new target.

- [ ] **Step 2.4: Commit**

```bash
git add plane/workflow/billing/bundle.go plane/workflow/billing/archive_schedules.go
git commit -m "$(cat <<'EOF'
feat(workflow): schedule targets ArchiveRouterWorkflow (#76)

Removes the ATTENTION block from archive_schedules.go.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Worker wiring helpers

**Files:**
- `cmd/workflow-worker/main.go` (new helper functions)

- [ ] **Step 3.1: Add `dialBillingService`**

Append to main.go:

```go
import (
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

func dialBillingService(addr string, allowInsecure bool) (*grpc.ClientConn, error) {
    if addr == "" {
        return nil, errors.New("BILLING_SERVICE_ADDR is empty")
    }
    var opts []grpc.DialOption
    if allowInsecure {
        opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
    } else {
        return nil, errors.New("only WORKER_BILLING_INSECURE=true is supported until SPIRE/SPIFFE wiring lands (ADR-010)")
    }
    return grpc.NewClient(addr, opts...)
}
```

- [ ] **Step 3.2: Add `buildS3ClientFromEnv`**

Mirror existing AWS-SDK-v2 patterns in the codebase (grep `aws.LoadDefaultConfig` or `awsconfig.LoadDefaultConfig`):

```go
import (
    "github.com/aws/aws-sdk-go-v2/aws"
    awsconfig "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/s3"
)

func buildS3ClientFromEnv(ctx context.Context) (*s3.Client, error) {
    cfg, err := awsconfig.LoadDefaultConfig(ctx,
        awsconfig.WithRegion(envDefault("S3_REGION", "us-east-1")),
    )
    if err != nil {
        return nil, fmt.Errorf("aws config: %w", err)
    }
    return s3.NewFromConfig(cfg, func(o *s3.Options) {
        if endpoint := os.Getenv("S3_ENDPOINT"); endpoint != "" {
            o.BaseEndpoint = aws.String(endpoint)
            o.UsePathStyle = true
        }
    }), nil
}
```

If a sibling cmd already has this helper, lift it instead of duplicating.

- [ ] **Step 3.3: Build**

```bash
go build ./cmd/workflow-worker
```

Expected: success.

- [ ] **Step 3.4: Commit**

```bash
git add cmd/workflow-worker/main.go
git commit -m "$(cat <<'EOF'
feat(workflow-worker): dialBillingService + buildS3ClientFromEnv helpers (#76)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Wire ArchiveDeps + schedule registration

**File:** `cmd/workflow-worker/main.go`

- [ ] **Step 4.1: Build the deps when S3_BUCKET set**

After existing `partActivity` construction and before `billing.Bundle(...).Apply(...)`:

```go
var archiveDeps *billing.ArchiveDeps
var billingConn *grpc.ClientConn
defer func() {
    if billingConn != nil {
        _ = billingConn.Close()
    }
}()

if bucket := os.Getenv("S3_BUCKET"); bucket != "" {
    var err error
    billingConn, err = dialBillingService(
        os.Getenv("BILLING_SERVICE_ADDR"),
        envBool("WORKER_BILLING_INSECURE", false),
    )
    if err != nil {
        return fmt.Errorf("billing dial: %w", err)
    }
    billingClient := appclient.NewGRPCBillingClient(billingConn)

    vaultClient, err := billing.LoadVaultClientFromEnv()
    if err != nil {
        return fmt.Errorf("vault client: %w", err)
    }
    keys := billing.NewVaultKeyProvider(
        vaultClient,
        envDefault("VAULT_TRANSIT_MOUNT", ""),
        envDefault("VAULT_BILLING_KEY", ""),
    )

    s3client, err := buildS3ClientFromEnv(ctx)
    if err != nil {
        return fmt.Errorf("s3 client: %w", err)
    }
    store := billing.NewS3ObjectStore(s3client, bucket)

    archiver := billingstore.NewPostgresArchiver(pool)
    detach, err := billing.NewDetachPartitionActivity(archiver)
    if err != nil { return fmt.Errorf("detach activity: %w", err) }
    drop, err := billing.NewDropPartitionActivity(archiver)
    if err != nil { return fmt.Errorf("drop activity: %w", err) }
    emit, err := billing.NewEmitArchiveEventActivity(billingClient)
    if err != nil { return fmt.Errorf("emit activity: %w", err) }
    export, err := billing.NewExportActivity(archiver, store, keys, bucket)
    if err != nil { return fmt.Errorf("export activity: %w", err) }
    archiveDeps = &billing.ArchiveDeps{Detach: detach, Drop: drop, Emit: emit, Export: export}
} else {
    logger.Info("S3_BUCKET unset; skipping archive deps + schedule")
}
```

- [ ] **Step 4.2: Pass deps to billing.Bundle**

Replace the existing `billing.Bundle(partActivity, nil).Apply(workerRegistrar{w})` call with `billing.Bundle(partActivity, archiveDeps).Apply(workerRegistrar{w})`. The Bundle handles `archiveDeps == nil` already (existing behaviour); the new path is just non-nil when env vars are set.

- [ ] **Step 4.3: Register the schedule**

After the existing `EnsureRolloverSchedule(...)` call:

```go
if archiveDeps != nil {
    if _, err := billing.EnsureArchiveSchedule(ctx, scheduleClient); err != nil {
        return fmt.Errorf("ensure archive schedule: %w", err)
    }
}
```

Use the existing `scheduleClient` already in scope.

- [ ] **Step 4.4: Build + tests**

```bash
go build ./cmd/workflow-worker
go test -race ./cmd/workflow-worker/... -count=1
```

Expected: PASS.

- [ ] **Step 4.5: Commit**

```bash
git add cmd/workflow-worker/main.go
git commit -m "$(cat <<'EOF'
feat(workflow-worker): wire ArchiveDeps + EnsureArchiveSchedule (#76)

Worker boots the four archive activities and registers the monthly
schedule when S3_BUCKET is set; falls back to the existing skip path
otherwise.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Integration test (heavy)

**File:** `cmd/workflow-worker/integration_test.go`

- [ ] **Step 5.1: Inspect existing harness**

```bash
ls cmd/workflow-worker/
cat cmd/workflow-worker/integration_test.go 2>/dev/null
```

If a harness exists, extend it. Otherwise create using the testcontainer
patterns from sibling packages (vault from #75, postgres from #74, minio
already used by archive_workflow tests).

- [ ] **Step 5.2: Write the test (`//go:build integration`)**

The test boots:
- testcontainer Postgres (with seeded migrations through 008/009)
- testcontainer Vault dev mode (with `transit/keys/platform-billing-master`)
- testcontainer minio (S3 endpoint)
- in-process billing-service gRPC server (or boot the real `cmd/billing-service` binary)
- Temporal devserver (use `temporal.io/sdk` testsuite if devserver is unavailable)

Then:
- Boot the worker (or invoke its `run(logger)` directly with env set).
- Trigger the archive schedule manually via the schedule client.
- Wait for completion.
- Assert: `billing.partition_archives` row exists; `billing.billing_outbox`
  row with `event_type=billing.partition_archived` exists; manifest +
  parquet visible in S3.

This is the largest single test in the codebase by infra surface. Keep
within `//go:build integration`. If devserver is unavailable in CI, mark
the test `t.Skip("requires Temporal devserver")` rather than removing it.

- [ ] **Step 5.3: Run**

```bash
go test -tags integration -race -run TestWorkerArchiveE2E ./cmd/workflow-worker/... -count=1
```

Expected: PASS (or skip with explicit reason).

- [ ] **Step 5.4: Commit**

```bash
git add cmd/workflow-worker/integration_test.go
git commit -m "$(cat <<'EOF'
test(workflow-worker): e2e archive path (#76)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Final gates + open PR

- [ ] **Step 6.1: Test sweep**

```bash
go build ./...
go vet ./...
golangci-lint run
go test -race ./... -count=1
go test -tags integration -race ./plane/workflow/billing/... ./cmd/workflow-worker/... -count=1
```

- [ ] **Step 6.2: Skills**

- `gitscale-temporal-determinism` — workflow.Now in router is replay-safe; verify
- `gitscale-go-conventions`
- `gitscale-plane-boundary` — workflow plane importing data plane is permitted by amended ADR-019 for read/DDL; this PR uses `billingstore.NewPostgresArchiver` (DDL — permitted)
- `gitscale-adr-guard`

- [ ] **Step 6.3: Self-review (parallel)**

- code-reviewer, silent-failure-hunter, type-design-analyzer, pr-test-analyzer, adr-historian, architect-review (cmd-level wiring is a plane-interface change → architect-review applies).

- [ ] **Step 6.4: Push + open PR**

```bash
git push -u origin feat/workflow-worker-archive-wiring
gh pr create --title "[Workflow] cmd/workflow-worker wiring for ArchiveDeps + EnsureArchiveSchedule Args" --body "$(cat <<'EOF'
## Summary

- `cmd/workflow-worker/main.go` builds real `*billing.ArchiveDeps` from env
  (gRPC billing client, Vault key provider, S3 store, Postgres archiver).
- New `ArchiveRouterWorkflow` works around Temporal's static
  `ScheduleWorkflowAction.Args` by computing `(year, month) = now − 18mo`
  at fire time and spawning `PartitionArchiveWorkflow` as a child.
- `EnsureArchiveSchedule` now targets the router; ATTENTION block removed.
- Integration test (heavy, build-tagged) asserts the full archive path.

## ADR-impact

conforming. Implements the production wiring deferred by #69 / PR #73 per
ADR-018 + ADR-019.

## Test plan

- [x] `go test -race ./plane/workflow/billing/...`
- [x] `go test -tags integration -race ./plane/workflow/billing/... ./cmd/workflow-worker/...`
- [x] Determinism: `gitscale-temporal-determinism` clean
- [x] Worker boots successfully when S3_BUCKET set; skips otherwise

Spec: docs/superpowers/specs/2026-05-08-issue-76-workflow-worker-archive-wiring-design.md
Plan: docs/superpowers/plans/2026-05-08-issue-76-workflow-worker-archive-wiring-plan.md

<details><summary>Self-review</summary>

- code-reviewer: <result>
- silent-failure-hunter: <result>
- type-design-analyzer: <result>
- pr-test-analyzer: <result>
- adr-historian: <result>
- architect-review: <result>

</details>

Closes #76.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 6.5: Watch CI**

---

## Self-review (plan author)

**Spec coverage:** ArchiveDeps wiring (Task 4), router workflow (Task 1),
schedule wiring (Task 2), integration test (Task 5), ATTENTION block
removal (Task 2.2).

**Placeholder scan:** Step 1.1 references `PartitionArchiveResult` —
implementer must verify the canonical name in `archive_workflow.go`. Step
2.1 leaves the exact `RegisterWorkflowWithOptions` shape to the
implementer to mirror existing conventions. Both are surface-level.

**Type consistency:** ArchiveRouterWorkflow, ArchiveRouterWorkflowName
referenced consistently. ArchiveInput/ArchiveResult names depend on the
existing package shape — verified during implementation.
