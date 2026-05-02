-- requires PostgreSQL 16+
-- Repositories domain: repo metadata, ACLs, topics, outbox.
-- org_id and owner_id are soft refs to identity domain (no FK by design).

CREATE SCHEMA IF NOT EXISTS repositories;

CREATE TABLE repositories.repositories (
  id UUID PRIMARY KEY,
  org_id UUID NOT NULL,        -- soft ref to identity.organisations(id)
  name TEXT NOT NULL,           -- human-readable display name
  slug TEXT NOT NULL,           -- URL path segment, must match charset constraint below
  owner_id UUID NOT NULL,       -- soft ref to identity.human_users(id) (creator)
  default_branch TEXT NOT NULL DEFAULT 'main',
  visibility TEXT NOT NULL DEFAULT 'private',
  fork_parent_id UUID REFERENCES repositories.repositories(id),
  lfs_enabled BOOLEAN NOT NULL DEFAULT false,
  replica_set_id TEXT,          -- ADR-009: identifies the Gitaly replica set holding this repo's primary
  home_region TEXT,             -- ADR-009: region anchor used by the Git proxy for replica selection
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (org_id, slug),
  CONSTRAINT chk_repositories_visibility_valid
    CHECK (visibility IN ('public', 'private', 'internal')),
  CONSTRAINT chk_repositories_slug_charset
    CHECK (slug ~ '^[a-z0-9][a-z0-9._-]*$'),
  CONSTRAINT chk_repositories_slug_length
    CHECK (LENGTH(slug) BETWEEN 1 AND 100)
);

-- Reserved-name list (e.g., 'new', 'settings') is enforced in application code, not SQL.

CREATE INDEX idx_repositories_org_slug
  ON repositories.repositories (org_id, slug);

CREATE INDEX idx_repositories_owner_id
  ON repositories.repositories (owner_id);
-- EXPLAIN: supports the "list repos owned by user" query at 100M-repo target (architecture.md §7.3).

CREATE INDEX idx_repositories_replica_routing
  ON repositories.repositories (replica_set_id, home_region);
-- EXPLAIN: supports ADR-009 Git proxy location lookups on cache miss.

CREATE TABLE repositories.repo_permissions (
  repo_id UUID NOT NULL REFERENCES repositories.repositories(id),
  principal_id UUID NOT NULL,           -- soft ref to identity (human/agent/org)
  principal_type TEXT NOT NULL,
  access_level TEXT NOT NULL,
  PRIMARY KEY (repo_id, principal_id, principal_type),
  CONSTRAINT chk_repo_permissions_principal_type_valid
    CHECK (principal_type IN ('human', 'agent', 'org')),
  CONSTRAINT chk_repo_permissions_access_level_valid
    CHECK (access_level IN ('read', 'write', 'admin'))
);

CREATE TABLE repositories.repo_topics (
  repo_id UUID NOT NULL REFERENCES repositories.repositories(id),
  topic TEXT NOT NULL,
  PRIMARY KEY (repo_id, topic)
);

CREATE TABLE repositories.repositories_outbox (
  id BIGSERIAL PRIMARY KEY,
  event_id UUID NOT NULL UNIQUE,
  aggregate_type TEXT NOT NULL,
  aggregate_id UUID NOT NULL,
  event_type TEXT NOT NULL,
  payload JSONB NOT NULL,
  processed_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_repositories_outbox_unprocessed
  ON repositories.repositories_outbox (created_at)
  WHERE processed_at IS NULL;
