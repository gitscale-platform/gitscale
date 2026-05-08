//go:build integration

package main_test

import (
	"testing"
)

// TestWorkerArchiveE2E is the heavy end-to-end test for the archive path
// wired by #76. It is intended to:
//
//   - boot testcontainer Postgres (with 007–009 migrations applied so
//     billing.partition_archives + billing_outbox exist)
//   - boot testcontainer Vault dev mode with transit/keys/platform-billing-master
//   - boot testcontainer minio (S3 endpoint)
//   - boot an in-process billing-service gRPC server backed by
//     billing.PostgresService against the same Postgres
//   - boot a Temporal devserver
//   - run the worker via cmd/workflow-worker.run with the env populated
//   - manually trigger ArchiveScheduleID
//   - assert: PartitionArchiveWorkflow run completes; partition_archives
//     row appears; billing_outbox row with event_type=billing.partition_archived
//     appears; manifest + parquet exist in S3
//
// The Temporal devserver dependency is not yet packaged for this repo's CI
// image — the constituent harness pieces exist (Postgres/Vault/minio
// containers in sibling integration_test.go files), and the activity-level
// integration coverage already lives in
// plane/workflow/billing/export_activity_vault_test.go and
// plane/workflow/billing/archive_workflow_test.go (testsuite mocks).
//
// Tracked: follow-up issue for Temporal devserver harness.
func TestWorkerArchiveE2E(t *testing.T) {
	t.Skip("requires Temporal devserver harness; tracked as follow-up to #76")
}
