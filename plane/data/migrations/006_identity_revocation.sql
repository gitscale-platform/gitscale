-- requires PostgreSQL 16+
-- Adds soft-revocation columns + per-table updated_at triggers consumed by
-- #15-revocation. Additive only; safe rollback by dropping the new columns.

-- Reusable updated_at bump function. The schema-domain follow-up (#46) will
-- migrate this to a public namespace shared across all five domains; until
-- then it lives under identity.
CREATE OR REPLACE FUNCTION identity.set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE identity.human_users
    ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS disable_reason TEXT;

ALTER TABLE identity.agent_identities
    ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS revoke_reason TEXT;

CREATE INDEX IF NOT EXISTS idx_human_users_disabled_at
    ON identity.human_users(disabled_at) WHERE disabled_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_agent_identities_revoked_at
    ON identity.agent_identities(revoked_at) WHERE revoked_at IS NOT NULL;

DROP TRIGGER IF EXISTS trg_human_users_updated_at ON identity.human_users;
CREATE TRIGGER trg_human_users_updated_at
    BEFORE UPDATE ON identity.human_users
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();

DROP TRIGGER IF EXISTS trg_agent_identities_updated_at ON identity.agent_identities;
CREATE TRIGGER trg_agent_identities_updated_at
    BEFORE UPDATE ON identity.agent_identities
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();
