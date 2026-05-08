# Issue #78 archive workflow integration test — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Add `archive_workflow_integration_test.go` with three test cases (happy path, crash resumption, DETACH PENDING recovery) under `//go:build integration`.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-78-archive-workflow-integration-test-design.md`

**Branch:** `test/workflow-archive-integration`

---

## File map

### Create
- `plane/workflow/billing/archive_workflow_integration_test.go`

### Modify
- (none — purely additive; reuse existing PostgresArchiver, S3ObjectStore, StubBillingClient, StubKeyProvider)

---

## Pre-flight

- [ ] **Step P.1: Worktree**

```bash
git worktree add -b test/workflow-archive-integration \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/test-workflow-archive-integration \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/test-workflow-archive-integration
```

- [ ] **Step P.2: Inspect existing helpers**

```bash
grep -rn "testcontainers\|setupPostgres\|setupS3\|setupMinio" plane/workflow/billing/ plane/data/store/postgres/ | head -20
```

Identify the canonical PG + minio testcontainer helpers. Reuse them; don't duplicate.

---

## Task 1: Happy-path test

- [ ] **Step 1.1: Write the test**

Mirror the existing `archive_workflow_test.go` shape (workflow testsuite), substituting real `PostgresArchiver` + `S3ObjectStore` for the unit-test stubs. Seed a partition with 100 rows, run the workflow, assert all artifacts.

The test scaffolding belongs in a `setupArchiveE2E(t)` helper at the top of the file (PG container, minio container, archiver, store, client).

- [ ] **Step 1.2: Run**

```bash
go test -tags integration -race -run TestArchiveWorkflow_E2E_HappyPath ./plane/workflow/billing/... -count=1
```

Expected: PASS.

- [ ] **Step 1.3: Commit**

```bash
git add plane/workflow/billing/archive_workflow_integration_test.go
git commit -m "test(workflow): archive workflow e2e happy path (#78)" --signoff
```

(Use the project's standard commit message convention; include
Co-Authored-By: Claude Sonnet 4.6 trailer.)

---

## Task 2: Crash-resumption test

- [ ] **Step 2.1: Add the test**

Append `TestArchiveWorkflow_E2E_CrashResumption`. Cancel the activity
context mid-stream; re-run; assert idempotent completion.

- [ ] **Step 2.2: Run + commit**

---

## Task 3: DETACH PENDING recovery test

- [ ] **Step 3.1: Add the test**

Append `TestArchiveWorkflow_E2E_DetachPendingRecovery`. Simulate
interrupted DETACH; re-run; verify recovery.

- [ ] **Step 3.2: Run + commit**

---

## Task 4: Final gates + open PR

- [ ] **Step 4.1: Test sweep**

```bash
go test -tags integration -race ./plane/workflow/billing/... -count=1
```

- [ ] **Step 4.2: Push + open PR**

```bash
git push -u origin test/workflow-archive-integration
gh pr create --title "[Workflow] Integration test for PartitionArchiveWorkflow against testcontainers PG + minio" --body "..."
```

Closes #78. Cross-link spec + plan files (copy them into the worktree first).
