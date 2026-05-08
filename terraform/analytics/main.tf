# STUB — provision via ops. Tracks issue #77 + ADR-018.
#
# This file documents the Glue Data Catalog resources that
# plane/workflow/billing/GlueRegisterActivity expects to exist. The full
# Terraform module is authored separately by the platform-ops team
# (out of scope per spec docs/superpowers/specs/2026-05-08-issue-77-...).
#
# When the module lands, uncomment + adapt. Until then this file serves as
# the contract between workflow code and infrastructure.

# resource "aws_glue_catalog_database" "gitscale_analytics" {
#   name        = "gitscale_analytics"
#   description = "GitScale archived billing partitions; queried via Athena."
# }
#
# resource "aws_glue_catalog_table" "usage_events" {
#   database_name = aws_glue_catalog_database.gitscale_analytics.name
#   name          = "usage_events"
#   table_type    = "EXTERNAL_TABLE"
#
#   partition_keys {
#     name = "year"
#     type = "string"
#   }
#   partition_keys {
#     name = "month"
#     type = "string"
#   }
#
#   storage_descriptor {
#     location      = "s3://${var.analytics_lake_bucket}/billing/usage_events/"
#     input_format  = "org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat"
#     output_format = "org.apache.hadoop.hive.ql.io.parquet.MapredParquetOutputFormat"
#
#     ser_de_info {
#       serialization_library = "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe"
#     }
#
#     # Columns mirror billing.usage_events; keep in sync with the PG schema
#     # owned by plane/data/store/billing.
#     columns {
#       name = "event_id"
#       type = "string"
#     }
#     columns {
#       name = "ts"
#       type = "timestamp"
#     }
#     # ... org_id, repo_id, kind, quantity, etc.
#   }
# }
#
# # IAM policy attached to the workflow-worker SPIFFE identity (ADR-010).
# # Minimum permissions for GlueRegisterActivity.
# data "aws_iam_policy_document" "workflow_worker_glue_write" {
#   statement {
#     actions = [
#       "glue:CreatePartition",
#       "glue:GetTable",
#       "glue:GetDatabase",
#     ]
#     resources = [
#       aws_glue_catalog_database.gitscale_analytics.arn,
#       aws_glue_catalog_table.usage_events.arn,
#       "arn:aws:glue:${var.region}:${var.account_id}:catalog",
#     ]
#   }
# }
#
# # IAM policy attached to analyst roles for read-only Athena access.
# data "aws_iam_policy_document" "analyst_glue_read" {
#   statement {
#     actions = [
#       "glue:GetDatabase",
#       "glue:GetTable",
#       "glue:GetPartition",
#       "glue:GetPartitions",
#     ]
#     resources = [
#       aws_glue_catalog_database.gitscale_analytics.arn,
#       aws_glue_catalog_table.usage_events.arn,
#       "arn:aws:glue:${var.region}:${var.account_id}:catalog",
#     ]
#   }
#   statement {
#     actions   = ["s3:GetObject", "s3:ListBucket"]
#     resources = [
#       "arn:aws:s3:::${var.analytics_lake_bucket}",
#       "arn:aws:s3:::${var.analytics_lake_bucket}/billing/usage_events/*",
#     ]
#   }
# }
