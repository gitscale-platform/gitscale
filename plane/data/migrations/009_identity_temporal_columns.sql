-- 009_identity_temporal_columns.sql
-- Schema-consistency fix: identity.org_memberships had no temporal columns;
-- identity.oauth_apps had created_at but no updated_at. Audit queries that
-- assume the standard pair on every identity table missed both. This
-- migration backfills the gap and wires both tables to the existing
-- identity.set_updated_at() trigger function (from 006_identity_revocation).
--
-- Idempotent: ADD COLUMN IF NOT EXISTS + DROP TRIGGER IF EXISTS make
-- re-application safe. Existing rows receive now() as their default for
-- both columns; no audit consumer depends on the pre-migration era.

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
