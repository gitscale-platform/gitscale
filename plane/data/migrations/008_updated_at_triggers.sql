-- 008_updated_at_triggers.sql
-- Generic updated_at = now() trigger across schema domains.
-- identity.human_users and identity.agent_identities are already covered
-- by 006_identity_revocation.sql; that function (identity.set_updated_at)
-- is left in place for that domain. Remaining tables that carry an
-- updated_at column are wired here so audit queries that filter by
-- updated_at observe actual mutations (closes silent-data-rot gap).
--
-- Idempotent: CREATE OR REPLACE on the function and DROP TRIGGER IF EXISTS
-- before each CREATE TRIGGER means re-application is safe.

BEGIN;

CREATE OR REPLACE FUNCTION public.set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

-- identity (human_users, agent_identities already covered by 006_identity_revocation.sql)
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
