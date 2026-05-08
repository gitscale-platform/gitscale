# Issue #46 updated_at trigger across 5 schema domains — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `public.set_updated_at()` trigger function and attach it to every existing table that already has an `updated_at` column but no auto-bump trigger.

**Architecture:** One SQL migration adding the function in the neutral `public` schema and seven `CREATE TRIGGER` statements (identity.organisations, repositories.repositories, collaboration.pull_requests/issues/comments, billing.quota_accounts/invoices). Compliance test asserts the bump per table.

**Tech Stack:** PostgreSQL, Go testcontainer compliance suite.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-46-updated-at-trigger-design.md`

**Branch:** `feat/data-updated-at-trigger` (worktree: `../gitscale.worktrees/feat-data-updated-at-trigger`)

---

## File map

### Create
- `plane/data/migrations/NNN_updated_at_triggers.sql` (NNN selected at rebase time; baseline is 007 if no other Wave-2 migrations have merged ahead)
- `plane/data/store/postgres/updated_at_trigger_test.go` — integration-tagged

### Modify
- `plane/data/store/postgres/compliance_test.go` — append the new migration filename to the `migrations := []string{...}` slice

---

## Pre-flight

- [ ] **Step P.1: Worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
git worktree add -b feat/data-updated-at-trigger \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-data-updated-at-trigger \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-data-updated-at-trigger
git status --porcelain
```

Expected: clean.

- [ ] **Step P.2: Identify the next migration number**

```bash
ls plane/data/migrations/ | grep -E '^[0-9]{3}_' | sort | tail -3
```

Pick the next available `NNN`. At spec-write time the answer is `007`.
At rebase, the implementer revisits this if intermediate migrations have
landed on `main`.

- [ ] **Step P.3: Baseline**

```bash
go build ./...
go test -tags integration -race ./plane/data/store/postgres/... -count=1
```

Expected: green.

---

## Task 1: Migration

**File:** `plane/data/migrations/NNN_updated_at_triggers.sql`

- [ ] **Step 1.1: Write the migration**

```sql
-- NNN_updated_at_triggers.sql
-- Generic updated_at = now() trigger across schema domains.
-- identity.human_users and identity.agent_identities are already covered
-- by 006_identity_revocation.sql; that function (identity.set_updated_at)
-- is left in place for that domain.

BEGIN;

CREATE OR REPLACE FUNCTION public.set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_organisations_updated_at ON identity.organisations;
CREATE TRIGGER trg_organisations_updated_at
    BEFORE UPDATE ON identity.organisations
    FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

DROP TRIGGER IF EXISTS trg_repositories_updated_at ON repositories.repositories;
CREATE TRIGGER trg_repositories_updated_at
    BEFORE UPDATE ON repositories.repositories
    FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

DROP TRIGGER IF EXISTS trg_pull_requests_updated_at ON collaboration.pull_requests;
CREATE TRIGGER trg_pull_requests_updated_at
    BEFORE UPDATE ON collaboration.pull_requests
    FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

DROP TRIGGER IF EXISTS trg_issues_updated_at ON collaboration.issues;
CREATE TRIGGER trg_issues_updated_at
    BEFORE UPDATE ON collaboration.issues
    FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

DROP TRIGGER IF EXISTS trg_comments_updated_at ON collaboration.comments;
CREATE TRIGGER trg_comments_updated_at
    BEFORE UPDATE ON collaboration.comments
    FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

DROP TRIGGER IF EXISTS trg_quota_accounts_updated_at ON billing.quota_accounts;
CREATE TRIGGER trg_quota_accounts_updated_at
    BEFORE UPDATE ON billing.quota_accounts
    FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

DROP TRIGGER IF EXISTS trg_invoices_updated_at ON billing.invoices;
CREATE TRIGGER trg_invoices_updated_at
    BEFORE UPDATE ON billing.invoices
    FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

COMMIT;
```

- [ ] **Step 1.2: Append filename to compliance_test migration list**

In `plane/data/store/postgres/compliance_test.go`, find the `migrations := []string{…}` slice. Append the new filename (e.g. `"007_updated_at_triggers.sql"`).

- [ ] **Step 1.3: Run compliance test**

```bash
go test -tags integration -race -run TestSchemaCompliance ./plane/data/store/postgres/... -count=1
```

Expected: PASS — migration applies cleanly, schema is consistent.

---

## Task 2: Trigger behaviour test

**File:** `plane/data/store/postgres/updated_at_trigger_test.go`

- [ ] **Step 2.1: Inspect each table's required NOT-NULL columns**

For every table in the cases list below, identify:
1. The minimal columns required to INSERT a row (the rest can default).
2. A harmless field to UPDATE.

Tables: `identity.organisations`, `repositories.repositories`,
`collaboration.pull_requests`, `collaboration.issues`,
`collaboration.comments`, `billing.quota_accounts`, `billing.invoices`.

```bash
grep -A 30 "^CREATE TABLE identity.organisations" plane/data/migrations/001_identity.sql
# repeat per table from 002, 003, 005
```

- [ ] **Step 2.2: Write the test**

```go
//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	storetest "github.com/gitscale-platform/gitscale/plane/data/store/postgres/postgrestest"
)

type triggerCase struct {
	name      string
	seed      string // SQL returning a row id
	updateSQL string // takes the id as $1
	idCol     string
}

func TestUpdatedAtTriggerBumpsOnUpdate(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)

	cases := []triggerCase{
		{
			name:      "identity.organisations",
			seed:      `INSERT INTO identity.organisations (id, slug) VALUES (gen_random_uuid(), 'trg-org') RETURNING id`,
			updateSQL: `UPDATE identity.organisations SET slug='trg-org-updated' WHERE id=$1`,
			idCol:     "id",
		},
		// ... fill in remaining 6 cases per Step 2.1's column inspection
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var id string
			if err := pool.QueryRow(ctx, tc.seed).Scan(&id); err != nil {
				t.Fatalf("seed: %v", err)
			}
			var before time.Time
			if err := pool.QueryRow(ctx, `SELECT updated_at FROM `+tc.name+` WHERE `+tc.idCol+`=$1`, id).Scan(&before); err != nil {
				t.Fatalf("read before: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
			if _, err := pool.Exec(ctx, tc.updateSQL, id); err != nil {
				t.Fatalf("update: %v", err)
			}
			var after time.Time
			if err := pool.QueryRow(ctx, `SELECT updated_at FROM `+tc.name+` WHERE `+tc.idCol+`=$1`, id).Scan(&after); err != nil {
				t.Fatalf("read after: %v", err)
			}
			if !after.After(before) {
				t.Fatalf("%s: updated_at not bumped (before=%v after=%v)", tc.name, before, after)
			}
		})
	}
}
```

- [ ] **Step 2.3: Run**

```bash
go test -tags integration -race -run TestUpdatedAtTrigger ./plane/data/store/postgres/... -count=1
```

Expected: PASS for all subtests.

- [ ] **Step 2.4: Commit (combined: migration + test)**

```bash
git add plane/data/migrations/NNN_updated_at_triggers.sql \
        plane/data/store/postgres/compliance_test.go \
        plane/data/store/postgres/updated_at_trigger_test.go
git commit -m "$(cat <<'EOF'
feat(data): public.set_updated_at trigger across 5 domains (#46)

Attaches BEFORE UPDATE trigger to identity.organisations,
repositories.repositories, collaboration.pull_requests/issues/comments,
billing.quota_accounts/invoices. Closes the silent-data-rot gap where
updated_at columns were never bumped on UPDATE.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Final gates + open PR

- [ ] **Step 3.1: Test sweep**

```bash
go build ./...
go vet ./...
golangci-lint run
go test -tags integration -race ./plane/data/store/postgres/... -count=1
```

- [ ] **Step 3.2: Skills (data plane)**

- `gitscale-go-conventions`
- `database-design:sql-pro`

- [ ] **Step 3.3: Self-review battery (parallel)**

- `pr-review-toolkit:code-reviewer`
- `pr-review-toolkit:silent-failure-hunter`
- `pr-review-toolkit:type-design-analyzer` (no new public Go types — likely no-op)
- `pr-review-toolkit:pr-test-analyzer`
- `adr-historian` (no ADR impact)

- [ ] **Step 3.4: Push + open PR**

```bash
git push -u origin feat/data-updated-at-trigger
gh pr create --title "[Data] Generic updated_at trigger across 5 schema domains" --body "$(cat <<'EOF'
## Summary

- Adds `public.set_updated_at()` trigger function and attaches it to seven
  tables that have an `updated_at` column but no bump trigger:
  identity.organisations, repositories.repositories,
  collaboration.pull_requests/issues/comments,
  billing.quota_accounts/invoices.
- Compliance test asserts each table's `updated_at` advances on UPDATE.

## ADR-impact

none.

## Test plan

- [x] `go test -tags integration -race ./plane/data/store/postgres/...` (compliance + per-table behaviour)
- [x] Migration is idempotent (DROP TRIGGER IF EXISTS + CREATE OR REPLACE)

Spec: docs/superpowers/specs/2026-05-08-issue-46-updated-at-trigger-design.md
Plan: docs/superpowers/plans/2026-05-08-issue-46-updated-at-trigger-plan.md

<details><summary>Self-review</summary>

- code-reviewer: <result>
- silent-failure-hunter: <result>
- type-design-analyzer: <result>
- pr-test-analyzer: <result>
- adr-historian: <result>

</details>

Closes #46.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3.5: Watch CI**

```bash
gh pr checks <number> --watch
```

---

## Self-review (plan author)

**Spec coverage:** function defined, triggers attached to all 7 tables,
compliance test, idempotency.

**Placeholder scan:** Step 2.1 directs the implementer to inspect each
table's required columns to fill in seed/update SQL. Acceptable — schema
is local context.

**Type consistency:** N/A (SQL migration; no Go types).
