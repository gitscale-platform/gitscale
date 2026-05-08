# terraform/analytics — Glue Data Catalog (stub)

Tracks issue #77 and ADR-018 § Query path.

This directory is a **documentation stub**. The actual Terraform module is
authored by the platform-ops team and lives in the ops infra repository.
The files here exist so that:

1. Workflow-plane developers can see, in-tree, what infrastructure
   `plane/workflow/billing/GlueRegisterActivity` depends on.
2. The ADR-018 query path has a code-tracked anchor for review.

## Resources expected to exist

| Resource | Name | Notes |
|---|---|---|
| `aws_glue_catalog_database` | `gitscale_analytics` | one per environment |
| `aws_glue_catalog_table` | `usage_events` | Hive-partitioned by `year`, `month`; parquet SerDe |
| S3 bucket | `${analytics_lake_bucket}` | also owns archive parquet objects (see plane/workflow/billing/objectstore_s3.go) |

## IAM identities

### Workflow worker (`spiffe://gitscale/workflow-worker`)

Permissions required by `GlueRegisterActivity`:

- `glue:CreatePartition`
- `glue:GetTable`
- `glue:GetDatabase`

Scoped to the `gitscale_analytics` database + `usage_events` table.
S3 write permissions are granted separately to the `ExportActivity`
SPIFFE identity (already provisioned for #76).

### Analyst role (`spiffe://gitscale/analyst-readonly`)

Permissions required for Athena to query archived partitions:

- `glue:GetDatabase`
- `glue:GetTable`
- `glue:GetPartition`
- `glue:GetPartitions`
- `s3:GetObject`, `s3:ListBucket` on `${analytics_lake_bucket}/billing/usage_events/*`

## Why not a full module here

ADR-008 establishes ops vs platform plane separation; cloud-account-level
IAM and Glue resources belong to ops. The contract surface is small
enough (database name, table name, parquet SerDe) that documenting it
here is sufficient. If the contract grows, promote this stub into a real
module.
