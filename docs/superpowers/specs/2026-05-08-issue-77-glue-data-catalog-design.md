# Spec — Issue #77 Glue Data Catalog registration activity

Date: 2026-05-08
Issue: #77
Plane: workflow + data (Terraform)
Priority: p2 (Wave 2; deps #76)
ADR-impact: conforming (ADR-018 §Query path)

## Goals

1. Register every archived monthly partition with the AWS Glue Data Catalog
   so Athena queries against `gitscale_analytics.usage_events` return rows
   for archived months.
2. Add `GlueRegisterActivity` to the workflow activity chain after
   `EmitOutbox` and before `DropPartition`.
3. Idempotent registration via `glue:CreatePartition` with
   `EntityNotFoundException`-on-replay handled.
4. Localstack-Glue integration test.

## Non-goals

- Authoring the full Terraform module (separate ops effort tracked
  externally; this PR delivers the workflow-side activity and a stub TF
  reference).
- IAM policy changes (delegated to ops).
- Hive metastore alternative (Glue is the chosen backend per ADR-018).

## Architecture

### Activity

`plane/workflow/billing/glue_register_activity.go`:

```go
type GlueClient interface {
    CreatePartition(ctx context.Context, in *glue.CreatePartitionInput, opts ...func(*glue.Options)) (*glue.CreatePartitionOutput, error)
}

type GlueRegisterActivity struct {
    client      GlueClient
    database    string // "gitscale_analytics"
    table       string // "usage_events"
}

func NewGlueRegisterActivity(client GlueClient, database, table string) (*GlueRegisterActivity, error)

type GlueRegisterInput struct {
    Year  int
    Month int
    LakeURI string // s3://bucket/prefix/year=YYYY/month=MM/
}

func (a *GlueRegisterActivity) Execute(ctx context.Context, in GlueRegisterInput) error
```

`Execute` calls `glue.CreatePartition` with:

- `DatabaseName: a.database`
- `TableName: a.table`
- `PartitionInput`:
  - `Values: ["YYYY", "MM"]`
  - `StorageDescriptor.Location: s3://bucket/prefix/year=YYYY/month=MM/`
  - `StorageDescriptor.InputFormat: "org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat"`
  - `StorageDescriptor.OutputFormat: "org.apache.hadoop.hive.ql.io.parquet.MapredParquetOutputFormat"`
  - `StorageDescriptor.SerdeInfo`: parquet hive serde

On `AlreadyExistsException` (Glue's idempotency-conflict error), return nil.
On any other error, return wrapped error so Temporal retries.

### Workflow change

`PartitionArchiveWorkflow` activity sequence becomes:

```
DetachPartition → ExportActivity → EmitArchiveEventActivity → GlueRegisterActivity → DropPartition
```

`Bundle.ArchiveDeps` gains `GlueRegister *GlueRegisterActivity`.

`cmd/workflow-worker/main.go` builds the Glue client from env (`AWS_REGION`,
shared AWS SDK config) and constructs the activity in the same env-gated
block as #76's other archive deps.

### Terraform reference

A `terraform/analytics/main.tf` stub is added documenting the required
resources (Glue database, Glue table with Hive partition spec, IAM policy
for the worker SPIFFE identity). Marked `# STUB — provision via ops` with
links to the issue and ADR-018. Full TF authoring is out of scope.

### Integration test

`plane/workflow/billing/glue_register_activity_integration_test.go`
(`//go:build integration`):

- Boot localstack with Glue enabled.
- Pre-create the `gitscale_analytics.usage_events` table.
- Run `GlueRegisterActivity.Execute` for `(2024, 5)`.
- Assert: `GetPartition(values=["2024","05"])` returns the registered row.
- Re-run; assert no error (idempotency).

## Acceptance checklist

- [ ] `GlueRegisterActivity` implemented (idempotent CreatePartition)
- [ ] Workflow updated to call it between `Emit` and `Drop`
- [ ] `ArchiveDeps.GlueRegister` field added; wired in cmd/workflow-worker
- [ ] localstack-Glue integration test asserts partition appears in catalog
- [ ] Terraform reference stub committed under `terraform/analytics/`
- [ ] PR description references ADR-018

## References

- ADR-018 §Query path
- aws-sdk-go-v2 `service/glue`
- localstack glue: https://docs.localstack.cloud/user-guide/aws/glue/
