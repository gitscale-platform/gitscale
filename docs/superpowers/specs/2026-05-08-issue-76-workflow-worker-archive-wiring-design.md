# Spec — Issue #76 cmd/workflow-worker ArchiveDeps + schedule wiring

Date: 2026-05-08
Issue: https://github.com/gitscale-platform/gitscale/issues/76
Plane: workflow
Priority: p1 (Wave 1; #74 + #75 merged)
ADR-impact: conforming (ADR-018 + ADR-019 already shipped; this PR makes the
production path live)

## Problem

`cmd/workflow-worker/main.go` calls `billing.Bundle(partActivity, nil)` —
archive deps `nil` — so the four archive activities are never registered;
the schedule registers without `Args`, so a fired run reaches
`ArchiveInput{}` and is rejected by the workflow's range guard. Net: the
archive workflow never runs in production.

Pre-conditions now met:
- #74 merged: `appclient.NewGRPCBillingClient(*grpc.ClientConn)` is real.
- #75 merged: `billing.NewVaultKeyProvider(client, mount, key)` is real.

## Goals

1. Build a real `*billing.ArchiveDeps` in `cmd/workflow-worker/main.go` when
   the relevant env vars are present.
2. Build the `S3ObjectStore` from `S3_BUCKET` + AWS env.
3. Wire a `*grpc.ClientConn` into `appclient.NewGRPCBillingClient` against
   `BILLING_SERVICE_ADDR`.
4. Build a `*vault.Client` via the existing `LoadVaultClientFromEnv`
   helper from #75 and pass to `NewVaultKeyProvider`.
5. Register the four archive activities through `billing.Bundle` and
   register the schedule via `EnsureArchiveSchedule` — with
   `ScheduleWorkflowAction.Args` computed at schedule-fire time
   (cmd-level glue, not workflow body) such that
   `(year, month) = fireTime − 18 months`.
6. Remove the `archive_schedules.go` ATTENTION block declaring args wiring
   pending.

## Non-goals

- Adding any retry/backoff to the gRPC dial (Temporal retry on activity
  failure is the layer that handles transient errors).
- Building a TLS-aware `*vault.Client` setup. Dev posture (`VAULT_TOKEN`
  env) is sufficient at this stage.
- Configuring SPIRE/SPIFFE mTLS for the gRPC dial. Until SPIRE rolls,
  `WORKER_BILLING_INSECURE=true` matches the existing
  `IDENTITY_SERVICE_INSECURE` flag's posture.

## Architecture

### Env vars (worker boot)

New env vars consumed by `cmd/workflow-worker`:

| Var | Required | Notes |
|---|---|---|
| `BILLING_SERVICE_ADDR` | yes when archive enabled | `host:port` of cmd/billing-service |
| `WORKER_BILLING_INSECURE` | dev | `true` to dial without TLS (matches identity convention) |
| `VAULT_ADDR` | yes when archive enabled | parsed by `LoadVaultClientFromEnv` |
| `VAULT_TOKEN` | dev | dev-mode auth |
| `VAULT_TRANSIT_MOUNT` | optional | default `transit` |
| `VAULT_BILLING_KEY` | optional | default `platform-billing-master` |
| `S3_BUCKET` | yes when archive enabled | bucket for the parquet+manifest files |
| `S3_ENDPOINT` | optional | for local minio dev |
| `S3_REGION` | optional | default `us-east-1` |

If `S3_BUCKET` is unset, the worker logs and skips archive registration —
the existing `POSTGRES_URL`-based gating pattern in main.go is the model.

### Wiring sketch

```go
// after pool setup, after partActivity construction:
var archiveDeps *billing.ArchiveDeps
if bucket := os.Getenv("S3_BUCKET"); bucket != "" {
    grpcConn, err := dialBillingService(os.Getenv("BILLING_SERVICE_ADDR"), envBool("WORKER_BILLING_INSECURE", false))
    if err != nil { return fmt.Errorf("billing dial: %w", err) }
    defer grpcConn.Close()
    billingClient := appclient.NewGRPCBillingClient(grpcConn)

    vaultClient, err := billing.LoadVaultClientFromEnv()
    if err != nil { return fmt.Errorf("vault client: %w", err) }
    keys := billing.NewVaultKeyProvider(
        vaultClient,
        envDefault("VAULT_TRANSIT_MOUNT", ""),
        envDefault("VAULT_BILLING_KEY", ""),
    )

    s3, err := buildS3ClientFromEnv(ctx)
    if err != nil { return fmt.Errorf("s3 client: %w", err) }
    store := billing.NewS3ObjectStore(s3, bucket)

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
}

billing.Bundle(partActivity, archiveDeps).Apply(workerRegistrar{w})

// schedule registration
if archiveDeps != nil {
    sc, err := scheduleClientFromTemporalClient(c) // existing helper
    if err != nil { return err }
    if _, err := billing.EnsureArchiveSchedule(ctx, sc); err != nil {
        return fmt.Errorf("ensure archive schedule: %w", err)
    }
}
```

`dialBillingService` and `buildS3ClientFromEnv` are local helpers in
main.go (or a new `internal/wiring` file in the same package — preference:
keep in main.go to mirror the existing pattern).

### Schedule Args wiring

`EnsureArchiveSchedule` currently registers `client.ScheduleSpec` with no
`Args`. The fix is in `archive_schedules.go`:

```go
return sc.Create(ctx, client.ScheduleOptions{
    ID: ScheduleIDArchive,
    Spec: client.ScheduleSpec{
        Calendars: []client.ScheduleCalendarSpec{ /* monthly cron */ },
    },
    Action: &client.ScheduleWorkflowAction{
        ID:        "billing-archive-monthly",
        Workflow:  PartitionArchiveWorkflow,
        TaskQueue: ...,
        Args: []any{
            // Compute (year, month) := schedule-fire-time − 18 months at fire time.
            // Temporal supports arg templates? — no, it doesn't. Workaround:
            // schedule fires a thin "router" workflow that computes the offset
            // and starts PartitionArchiveWorkflow via ExecuteChildWorkflow.
        },
    },
})
```

Temporal's `ScheduleWorkflowAction.Args` is statically bound at schedule
creation, so the "fireTime − 18 months" computation must happen at fire
time. Two patterns:

1. **Router workflow** — the schedule starts `ArchiveRouterWorkflow(ctx)`,
   which uses `workflow.Now(ctx)` (deterministic — replay-safe), computes
   the target `(year, month)`, then `workflow.ExecuteChildWorkflow(...,
   PartitionArchiveWorkflow, ArchiveInput{Year, Month}).Get(ctx, &result)`.
   The router is a one-line child-spawn.
2. **Static args + monthly cron** — register a fresh schedule each month
   with the next month's `(year, month)` baked in. Heavier ops cost; rejected.

**Pick router workflow.** Determinism preserved (`workflow.Now` is the
canonical clock); router can be tested with the workflow testsuite.

New file: `plane/workflow/billing/archive_router_workflow.go`:

```go
const ArchiveRouterWorkflowName = "billing.ArchiveRouter"

func ArchiveRouterWorkflow(ctx workflow.Context) error {
    now := workflow.Now(ctx)
    target := now.AddDate(0, -18, 0)
    year, month := target.Year(), int(target.Month())
    cwo := workflow.ChildWorkflowOptions{WorkflowID: fmt.Sprintf("billing-archive-%04d-%02d", year, month)}
    cctx := workflow.WithChildOptions(ctx, cwo)
    var result PartitionArchiveResult
    return workflow.ExecuteChildWorkflow(cctx, PartitionArchiveWorkflow, ArchiveInput{Year: year, Month: month}).Get(ctx, &result)
}
```

`Bundle.Apply` registers it alongside the existing workflows.
`EnsureArchiveSchedule` targets `ArchiveRouterWorkflow` instead of
`PartitionArchiveWorkflow`.

### Integration test

`cmd/workflow-worker/integration_test.go` (testsuite-based, in-process):

- Boot worker against testcontainer PG + minio + Vault + (in-process)
  billing-service gRPC server backed by `billing.PostgresService`.
- Trigger schedule manually via `tctl schedule trigger`.
- Assert: workflow run completes; `billing.partition_archives` row appears;
  `billing.billing_outbox` row appears; manifest exists in S3.

This is heavy — run it under `//go:build integration` only.

## Test plan

| Layer | Test |
|---|---|
| Unit | Router workflow — fixed `workflow.Now` via testsuite; verify child started with correct (year, month) |
| Unit | Wiring helpers (`dialBillingService`, `buildS3ClientFromEnv`) — table-driven on env vars |
| Integration | Worker boots with archive deps when env complete; deps absent when `S3_BUCKET` unset |
| Existing | All current `cmd/workflow-worker` tests still pass |

## Acceptance checklist

- [ ] `cmd/workflow-worker/main.go` builds real `ArchiveDeps` from env
- [ ] gRPC client + Vault client + S3 client all wired
- [ ] `ArchiveRouterWorkflow` introduced; schedule targets it
- [ ] `EnsureArchiveSchedule` Args wiring resolved (via router)
- [ ] `archive_schedules.go` ATTENTION block removed
- [ ] Integration test asserts the full archive path
- [ ] PR description references ADR-018 + ADR-019

## Open questions

None.

## References

- `plane/workflow/billing/bundle.go::ArchiveDeps`
- `plane/workflow/billing/archive_schedules.go::EnsureArchiveSchedule`
- `plane/workflow/billing/emit_activity.go`, `export_activity.go`,
  `detach_activity.go`, `drop_activity.go`
- `plane/workflow/appclient/billing_grpc.go` (#74 PR #87)
- `plane/workflow/billing/vault_keyprovider.go` (#75 PR #92)
- ADR-018, ADR-019 in `docs/architecture.md §8`
