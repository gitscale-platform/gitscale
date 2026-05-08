# Issue #77 Glue Data Catalog registration — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Add `GlueRegisterActivity`, insert it into `PartitionArchiveWorkflow` between Emit and Drop, wire from `cmd/workflow-worker`, and integration-test against localstack.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-77-glue-data-catalog-design.md`

**Branch:** `feat/workflow-glue-register-activity`

---

## File map

### Create
- `plane/workflow/billing/glue_register_activity.go`
- `plane/workflow/billing/glue_register_activity_test.go`
- `plane/workflow/billing/glue_register_activity_integration_test.go`
- `terraform/analytics/main.tf` (stub)
- `terraform/analytics/README.md` (stub)

### Modify
- `plane/workflow/billing/archive_workflow.go` — insert GlueRegister call between EmitArchiveEvent and DropPartition
- `plane/workflow/billing/archive_workflow_test.go` — adapt mock chain to include GlueRegister
- `plane/workflow/billing/bundle.go` — add `GlueRegister *GlueRegisterActivity` to `ArchiveDeps`; register activity
- `cmd/workflow-worker/main.go` — build Glue client + GlueRegisterActivity in the env-gated archive block (Wave-1 #76 introduced this block)
- `go.mod`, `go.sum` — add `github.com/aws/aws-sdk-go-v2/service/glue`

---

## Pre-flight

- [ ] **Step P.1: Worktree from latest origin/main**
- [ ] **Step P.2: Verify baseline tests pass**

---

## Task 1: Activity + unit test

- [ ] **Step 1.1: glue_register_activity.go**

Implementation per spec. `GlueClient` interface narrows the AWS SDK
surface for testability. `Execute` calls `CreatePartition`; treats
`AlreadyExistsException` as success.

- [ ] **Step 1.2: glue_register_activity_test.go**

Unit test using a `fakeGlueClient` that records calls. Cases: happy path,
already-exists conflict (idempotent), other error (propagated).

- [ ] **Step 1.3: Build + test + commit**

---

## Task 2: Workflow integration

- [ ] **Step 2.1: Insert into archive_workflow.go**

Between `EmitArchiveEventActivity` and `DropPartitionActivity`, add
`GlueRegisterActivity.Execute`. Determinism preserved (sequential
activities).

- [ ] **Step 2.2: Update archive_workflow_test.go**

Mock chain gains the GlueRegister step.

- [ ] **Step 2.3: bundle.go — `ArchiveDeps.GlueRegister`**

Add the field; register the activity name in `Apply`.

- [ ] **Step 2.4: Build + run all unit tests + commit**

---

## Task 3: Worker wiring

- [ ] **Step 3.1: cmd/workflow-worker/main.go**

In the env-gated archive block (introduced by #76), build:

```go
glueClient := glue.NewFromConfig(awsCfg)
glueRegister, err := billing.NewGlueRegisterActivity(glueClient, "gitscale_analytics", "usage_events")
```

Append to `archiveDeps.GlueRegister`.

- [ ] **Step 3.2: go.mod**

```bash
go get github.com/aws/aws-sdk-go-v2/service/glue@latest
go mod tidy
```

- [ ] **Step 3.3: Build + commit**

---

## Task 4: localstack integration test

- [ ] **Step 4.1: glue_register_activity_integration_test.go**

testcontainer for localstack with `SERVICES=glue`. Pre-create the database
and table. Run the activity. Assert via `GetPartition`. Re-run to verify
idempotency.

- [ ] **Step 4.2: Run + commit**

---

## Task 5: Terraform stubs

- [ ] **Step 5.1: terraform/analytics/main.tf**

```hcl
# STUB — provision via ops. Tracks issue #77 + ADR-018.
# resource "aws_glue_catalog_database" "gitscale_analytics" {
#   name = "gitscale_analytics"
# }
# resource "aws_glue_catalog_table" "usage_events" {
#   database_name = aws_glue_catalog_database.gitscale_analytics.name
#   name          = "usage_events"
#   partition_keys = [
#     { name = "year",  type = "string" },
#     { name = "month", type = "string" },
#   ]
#   storage_descriptor { ... }   # parquet hive serde
# }
```

- [ ] **Step 5.2: terraform/analytics/README.md** documents the resource list and the IAM policy required by the workflow worker SPIFFE identity (`glue:CreatePartition`) and analyst role (`glue:GetPartition` + S3 read).

---

## Task 6: Final gates + open PR

- [ ] Test sweep, skills (`gitscale-go-conventions`, `gitscale-temporal-determinism`, `gitscale-plane-boundary`, `gitscale-adr-guard`), self-review battery, push, `gh pr create`. Closes #77. Cross-link spec + plan.
