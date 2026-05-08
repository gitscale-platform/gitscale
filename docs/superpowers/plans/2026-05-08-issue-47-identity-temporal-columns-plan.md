# Issue #47 identity temporal columns — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Add `created_at`+`updated_at` to `identity.org_memberships`; add `updated_at` to `identity.oauth_apps`; attach `identity.set_updated_at()` trigger to both.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-47-identity-temporal-columns-design.md`

**Branch:** `feat/data-identity-temporal-columns`

---

## File map

### Create
- `plane/data/migrations/NNN_identity_temporal_columns.sql`
- `plane/data/store/postgres/identity_temporal_columns_test.go`

### Modify
- `plane/data/store/postgres/compliance_test.go` — append to migrations slice

---

## Pre-flight

- [ ] **Step P.1: Worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
git worktree add -b feat/data-identity-temporal-columns \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-data-identity-temporal-columns \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-data-identity-temporal-columns
git status --porcelain
```

- [ ] **Step P.2: Pick NNN**

```bash
ls plane/data/migrations/ | grep -E '^[0-9]{3}_' | sort | tail -3
```

Use the next available number. At spec-write time the answer is `007`.

---

## Task 1: Migration

**File:** `plane/data/migrations/NNN_identity_temporal_columns.sql`

- [ ] **Step 1.1: Write**

```sql
BEGIN;

ALTER TABLE identity.org_memberships
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

DROP TRIGGER IF EXISTS trg_org_memberships_updated_at ON identity.org_memberships;
CREATE TRIGGER trg_org_memberships_updated_at
    BEFORE UPDATE ON identity.org_memberships
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();

ALTER TABLE identity.oauth_apps
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

DROP TRIGGER IF EXISTS trg_oauth_apps_updated_at ON identity.oauth_apps;
CREATE TRIGGER trg_oauth_apps_updated_at
    BEFORE UPDATE ON identity.oauth_apps
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();

COMMIT;
```

- [ ] **Step 1.2: Append filename to compliance_test slice**

In `plane/data/store/postgres/compliance_test.go`, append the new migration filename.

- [ ] **Step 1.3: Run compliance**

```bash
go test -tags integration -race -run TestSchemaCompliance ./plane/data/store/postgres/... -count=1
```

Expected: PASS.

---

## Task 2: Behaviour test

**File:** `plane/data/store/postgres/identity_temporal_columns_test.go`

- [ ] **Step 2.1: Write**

```go
//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	storetest "github.com/gitscale-platform/gitscale/plane/data/store/postgres/postgrestest"
)

func TestOrgMemberships_TemporalColumnsBumpOnUpdate(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)

	// Seed: org + user + membership.
	var orgID, userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO identity.organisations (id, slug) VALUES (gen_random_uuid(), 'org-temporal') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO identity.human_users (id, email, credential_hash) VALUES (gen_random_uuid(), 'temporal@example.com', 'x') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO identity.org_memberships (org_id, user_id, role) VALUES ($1, $2, 'developer')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	var before time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM identity.org_memberships WHERE org_id=$1 AND user_id=$2`, orgID, userID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := pool.Exec(ctx,
		`UPDATE identity.org_memberships SET role='maintainer' WHERE org_id=$1 AND user_id=$2`, orgID, userID); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM identity.org_memberships WHERE org_id=$1 AND user_id=$2`, orgID, userID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.After(before) {
		t.Fatalf("updated_at not bumped: before=%v after=%v", before, after)
	}
}

func TestOAuthApps_UpdatedAtBumpsOnUpdate(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)

	var orgID, appID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO identity.organisations (id, slug) VALUES (gen_random_uuid(), 'org-oauth') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO identity.oauth_apps (id, org_id, name, client_id, client_secret_hash) VALUES (gen_random_uuid(), $1, 'app', 'cid-1', 'h') RETURNING id`, orgID).Scan(&appID); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM identity.oauth_apps WHERE id=$1`, appID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := pool.Exec(ctx, `UPDATE identity.oauth_apps SET name='app-2' WHERE id=$1`, appID); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM identity.oauth_apps WHERE id=$1`, appID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.After(before) {
		t.Fatalf("updated_at not bumped: before=%v after=%v", before, after)
	}
}
```

(If the actual column for `human_users.credential_hash` differs, mirror
exactly what `001_identity.sql` declares — the schema is local context.)

- [ ] **Step 2.2: Run**

```bash
go test -tags integration -race -run "TestOrgMemberships_TemporalColumnsBumpOnUpdate|TestOAuthApps_UpdatedAtBumpsOnUpdate" ./plane/data/store/postgres/... -count=1
```

Expected: PASS.

- [ ] **Step 2.3: Commit**

```bash
git add plane/data/migrations/ plane/data/store/postgres/
git commit -m "$(cat <<'EOF'
feat(data): created_at/updated_at on org_memberships + oauth_apps (#47)

Backfills the schema-consistency gap: org_memberships gains both temporal
columns; oauth_apps gains updated_at. Both wired to
identity.set_updated_at() trigger from 006_identity_revocation.

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

- [ ] **Step 3.2: Skills**

- `gitscale-go-conventions`
- `database-design:sql-pro`

- [ ] **Step 3.3: Self-review battery**

- code-reviewer, silent-failure-hunter, type-design-analyzer, pr-test-analyzer, adr-historian.

- [ ] **Step 3.4: Push + open PR**

```bash
git push -u origin feat/data-identity-temporal-columns
gh pr create --title "[Data] Add created_at/updated_at to identity.org_memberships and oauth_apps" --body "$(cat <<'EOF'
## Summary

- Adds `created_at`+`updated_at` to `identity.org_memberships`.
- Adds `updated_at` to `identity.oauth_apps`.
- Attaches `identity.set_updated_at()` trigger to both.

## ADR-impact

none.

## Test plan

- [x] Compliance test bootstraps PG with the new migration
- [x] UPDATE on each table bumps `updated_at`
- [x] Migration is idempotent (`ADD COLUMN IF NOT EXISTS`, `DROP TRIGGER IF EXISTS`)

Spec: docs/superpowers/specs/2026-05-08-issue-47-identity-temporal-columns-design.md
Plan: docs/superpowers/plans/2026-05-08-issue-47-identity-temporal-columns-plan.md

<details><summary>Self-review</summary>

- code-reviewer: <result>
- silent-failure-hunter: <result>
- type-design-analyzer: <result>
- pr-test-analyzer: <result>
- adr-historian: <result>

</details>

Closes #47.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review (plan author)

**Spec coverage:** all six acceptance items map to Tasks 1, 2, and 3.

**Placeholder scan:** none.

**Type consistency:** N/A.
