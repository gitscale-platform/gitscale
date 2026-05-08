# Spec — Issue #46 Generic updated_at trigger across 5 schema domains

Date: 2026-05-08
Issue: https://github.com/gitscale-platform/gitscale/issues/46
Plane: data
Priority: p2 (Wave 0)
ADR-impact: none (silent-data-rot fix)

## Problem

Most tables across the five schema domains carry an `updated_at TIMESTAMPTZ
NOT NULL DEFAULT now()` column, but no trigger bumps it on UPDATE. The
column stays at insert-time forever — silent data rot. Audit queries that
filter by `updated_at` miss every change. Migration `006_identity_revocation.sql`
already defined `identity.set_updated_at()` and attached it to
`identity.human_users` and `identity.agent_identities`. The remaining
domains (and `identity.organisations`) are uncovered.

## Goals

1. One generic trigger function callable from any schema. Place it under a
   neutral schema (`public`) so each domain attaches without re-defining.
2. Attach the trigger to every existing table that has an `updated_at`
   column and is not yet covered.
3. Idempotent migration (safe to re-apply on a fresh DB or against a partly-
   migrated environment).
4. Compliance-test coverage: a single integration test asserts UPDATE bumps
   `updated_at` for every covered table.

## Non-goals

- Adding `updated_at` to tables that lack it (that's #47 and similar
  follow-ups; this spec is about wiring the trigger to *existing* columns).
- Removing the existing `identity.set_updated_at()` function. Leave it in
  place to avoid coupling this PR to a callsite refactor; deprecate later
  in a follow-up if desired.
- Triggering on outbox tables — outbox rows are write-once except for
  `processed_at`; bumping `updated_at` would muddle replay semantics. (Outbox
  tables don't carry `updated_at` anyway.)

## Architecture

### Function

`public.set_updated_at()`:

```sql
CREATE OR REPLACE FUNCTION public.set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;
```

Placed in `public` so triggers can reference one definition regardless of
the table's schema. The existing `identity.set_updated_at()` is left
untouched (see Non-goals).

### Migration

File: `plane/data/migrations/NNN_updated_at_triggers.sql` where `NNN` is
the next sequential number at PR-rebase time. (At spec-write time, master
is at 006; in-flight Wave-2 branches add their own — the rebase routine
selects the final number.)

```sql
BEGIN;

CREATE OR REPLACE FUNCTION public.set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

-- identity.organisations (human_users, agent_identities already covered by 006_identity_revocation.sql)
DROP TRIGGER IF EXISTS trg_organisations_updated_at ON identity.organisations;
CREATE TRIGGER trg_organisations_updated_at
    BEFORE UPDATE ON identity.organisations
    FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

-- repositories
DROP TRIGGER IF EXISTS trg_repositories_updated_at ON repositories.repositories;
CREATE TRIGGER trg_repositories_updated_at
    BEFORE UPDATE ON repositories.repositories
    FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

-- collaboration
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

-- billing
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

### CI tables

Inspecting `004_ci.sql`: `workflow_runs`, `ci_jobs`, `runner_assignments`,
`ci_outbox`. **None of these have `updated_at`.** The CI tables track
state-machine transitions via dedicated `started_at`, `completed_at`,
`assigned_at` columns. So the CI domain is correctly uncovered by this
trigger; no change needed.

### Compliance test

Append to `plane/data/store/postgres/compliance_test.go` (or a new
`updated_at_trigger_test.go` in the same package, build tag `integration`):

```go
//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	storetest "github.com/gitscale-platform/gitscale/plane/data/store/postgres/postgrestest"
)

func TestUpdatedAtTriggerBumpsOnUpdate(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)

	cases := []struct {
		schema, table string
		seed          string
		updateSQL     string
	}{
		{"identity", "organisations", `INSERT INTO identity.organisations (id, slug) VALUES (gen_random_uuid(), 'org-test') RETURNING id`, `UPDATE identity.organisations SET slug='org-test-2' WHERE id=$1`},
		{"repositories", "repositories", /* …seed + update, mirror the schema */},
		{"collaboration", "pull_requests", /* … */},
		{"collaboration", "issues", /* … */},
		{"collaboration", "comments", /* … */},
		{"billing", "quota_accounts", /* … */},
		{"billing", "invoices", /* … */},
	}
	for _, tc := range cases {
		t.Run(tc.schema+"."+tc.table, func(t *testing.T) {
			var id string
			if err := pool.QueryRow(ctx, tc.seed).Scan(&id); err != nil {
				t.Fatal(err)
			}
			var before, after time.Time
			pool.QueryRow(ctx, "SELECT updated_at FROM "+tc.schema+"."+tc.table+" WHERE id=$1", id).Scan(&before)
			time.Sleep(10 * time.Millisecond)
			if _, err := pool.Exec(ctx, tc.updateSQL, id); err != nil {
				t.Fatal(err)
			}
			pool.QueryRow(ctx, "SELECT updated_at FROM "+tc.schema+"."+tc.table+" WHERE id=$1", id).Scan(&after)
			if !after.After(before) {
				t.Fatalf("%s.%s: updated_at not bumped (before=%v after=%v)", tc.schema, tc.table, before, after)
			}
		})
	}
}
```

The implementer fills in real `seed` and `updateSQL` strings per each
table's column shape — the schema is local context.

## Test plan

| Layer | Test |
|---|---|
| Migration apply | Compliance test bootstraps PG with all migrations including the new one |
| Trigger behaviour | Per-table UPDATE bumps `updated_at` |
| Idempotency | Re-apply migration in the same session: no error |

## Acceptance checklist

- [ ] `public.set_updated_at()` defined
- [ ] Trigger attached to every existing table with `updated_at`
- [ ] Compliance test demonstrates bump on UPDATE
- [ ] Migration is `BEGIN`+`COMMIT` and uses `DROP TRIGGER IF EXISTS` for idempotency
- [ ] PR description lists the seven covered tables explicitly

## Open questions

None.

## References

- Existing `identity.set_updated_at()` in `006_identity_revocation.sql`
- Tables enumerated by grep over `plane/data/migrations/*.sql`
