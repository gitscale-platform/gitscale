# Spec — Issue #47 created_at/updated_at on identity.org_memberships + oauth_apps

Date: 2026-05-08
Issue: https://github.com/gitscale-platform/gitscale/issues/47
Plane: data
Priority: p2 (Wave 0)
ADR-impact: none (schema consistency fix)

## Problem

`identity.org_memberships` has neither `created_at` nor `updated_at`.
`identity.oauth_apps` has `created_at` but no `updated_at`. Audit queries
that assume every identity table carries the standard temporal pair miss
both. Fixing the gap is a one-shot ALTER + trigger attach.

## Goals

1. Add `created_at` + `updated_at` to `identity.org_memberships`.
2. Add `updated_at` to `identity.oauth_apps`.
3. Attach the existing `identity.set_updated_at()` trigger (from
   `006_identity_revocation.sql`) to both tables.
4. Compliance test asserts both columns exist and the trigger bumps
   `updated_at` on UPDATE.

## Non-goals

- Backfill historical rows. The defaults set `created_at = now()` and
  `updated_at = now()` on every existing row — close enough; no audit
  consumer depends on the pre-migration era.
- Renaming the trigger function to `public.set_updated_at()`. That depends
  on #46; this PR uses `identity.set_updated_at()` (which already exists on
  `main`) so it can ship independently.

## Architecture

### Migration

File: `plane/data/migrations/NNN_identity_temporal_columns.sql` (NNN
selected at rebase time).

```sql
BEGIN;

-- org_memberships gains both columns.
ALTER TABLE identity.org_memberships
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

DROP TRIGGER IF EXISTS trg_org_memberships_updated_at ON identity.org_memberships;
CREATE TRIGGER trg_org_memberships_updated_at
    BEFORE UPDATE ON identity.org_memberships
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();

-- oauth_apps gains updated_at and the trigger.
ALTER TABLE identity.oauth_apps
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

DROP TRIGGER IF EXISTS trg_oauth_apps_updated_at ON identity.oauth_apps;
CREATE TRIGGER trg_oauth_apps_updated_at
    BEFORE UPDATE ON identity.oauth_apps
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();

COMMIT;
```

`ADD COLUMN IF NOT EXISTS` keeps the migration safe to re-apply.

### Compliance test

Append to `plane/data/store/postgres/compliance_test.go`'s migrations list:
the new file.

Add per-table behaviour test in
`plane/data/store/postgres/identity_temporal_columns_test.go`
(`//go:build integration`):

```go
func TestOrgMembershipsTemporalColumnsBumpOnUpdate(t *testing.T) {
    // INSERT a row, snapshot updated_at, UPDATE role, assert updated_at advanced
}
func TestOAuthAppsUpdatedAtBumpsOnUpdate(t *testing.T) {
    // INSERT a row, snapshot updated_at, UPDATE name, assert updated_at advanced
}
```

### Optional store-layer alignment

`plane/data/store/metadata.go::OrgMembership` (struct) currently has only
`OrgID`, `UserID`, `Role`. Adding `CreatedAt time.Time` and `UpdatedAt
time.Time` is **out of scope** for #47 — would force changes to every
caller. That's a follow-up if/when the application plane needs the values.
The migration is forward-compatible: the column exists, and the store can
opt in to selecting it later without a migration.

## Test plan

| Layer | Test |
|---|---|
| Compliance | new migration applies cleanly on fresh PG |
| Per-table | UPDATE org_memberships → updated_at advances; same for oauth_apps |
| Idempotency | re-apply migration — no error, no schema diff |

## Acceptance checklist

- [ ] `created_at` + `updated_at` present on `identity.org_memberships`
- [ ] `updated_at` present on `identity.oauth_apps`
- [ ] `identity.set_updated_at()` trigger attached to both tables
- [ ] Compliance test bootstraps PG with the new migration
- [ ] Per-table behaviour tests pass
- [ ] Migration uses `IF NOT EXISTS` for column adds and `DROP TRIGGER IF
      EXISTS` for trigger creates

## Open questions

None.

## References

- `plane/data/migrations/001_identity.sql` (table definitions)
- `plane/data/migrations/006_identity_revocation.sql` (existing trigger function)
