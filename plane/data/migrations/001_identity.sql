-- requires PostgreSQL 16+
-- Identity domain: humans, organisations, agent identities, OAuth apps, outbox.
-- All cross-domain refs (quota_account_id) are soft UUID columns; FK enforcement is application-side.

CREATE SCHEMA IF NOT EXISTS identity;

CREATE TABLE identity.human_users (
  id UUID PRIMARY KEY,
  email TEXT UNIQUE NOT NULL,
  credential_hash TEXT NOT NULL,
  rate_bucket TEXT NOT NULL DEFAULT 'human_default',
  quota_account_id UUID,  -- soft ref to billing.quota_accounts(id)
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE identity.organisations (
  id UUID PRIMARY KEY,
  slug TEXT UNIQUE NOT NULL,
  display_name TEXT,
  quota_account_id UUID,  -- soft ref to billing.quota_accounts(id)
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE identity.org_memberships (
  org_id UUID NOT NULL REFERENCES identity.organisations(id),
  user_id UUID NOT NULL REFERENCES identity.human_users(id),
  role TEXT NOT NULL,
  PRIMARY KEY (org_id, user_id),
  CONSTRAINT chk_org_memberships_role_valid
    CHECK (role IN ('owner', 'maintainer', 'developer', 'reporter', 'guest'))
  -- system roles only; commercial edition adds custom-role table separately
);

CREATE TABLE identity.agent_identities (
  id UUID PRIMARY KEY,
  display_name TEXT,
  parent_user_id UUID NOT NULL REFERENCES identity.human_users(id),
  permission_scope TEXT[] NOT NULL DEFAULT '{}',
  rate_bucket TEXT NOT NULL DEFAULT 'agent_standard',
  session_quota BIGINT,
  tokens_per_week_cap BIGINT,
  reputation_score NUMERIC(5,4) NOT NULL DEFAULT 0.5
    CONSTRAINT chk_agent_identities_reputation_score_range
    CHECK (reputation_score >= 0 AND reputation_score <= 1),
  quota_account_id UUID,  -- soft ref to billing.quota_accounts(id)
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_identities_parent_user_id
  ON identity.agent_identities (parent_user_id);

CREATE TABLE identity.oauth_apps (
  id UUID PRIMARY KEY,
  org_id UUID NOT NULL REFERENCES identity.organisations(id),
  name TEXT NOT NULL,
  client_id TEXT UNIQUE NOT NULL,
  client_secret_hash TEXT NOT NULL,
  redirect_uris TEXT[] NOT NULL DEFAULT '{}',
  scopes TEXT[] NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE identity.identity_outbox (
  id BIGSERIAL PRIMARY KEY,
  event_id UUID NOT NULL UNIQUE,
  aggregate_type TEXT NOT NULL,
  aggregate_id UUID NOT NULL,
  event_type TEXT NOT NULL,
  payload JSONB NOT NULL,
  processed_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_identity_outbox_unprocessed
  ON identity.identity_outbox (created_at)
  WHERE processed_at IS NULL;

-- Hash-partition human_users on id when row count > 100M. Start unpartitioned.
-- Upgrade path: ALTER table to attach hash partitions, requires PG12+ FK semantics (PG16+ here).
