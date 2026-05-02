-- requires PostgreSQL 16+
-- Collaboration domain: PRs, reviews, issues, comments, outbox.
-- Core file is unpartitioned for CRDB compat; PG overlay 003_collaboration_part.sql
-- converts pull_requests and issues to hash-partitioned tables.

CREATE SCHEMA IF NOT EXISTS collaboration;

CREATE TABLE collaboration.pull_requests (
  id UUID PRIMARY KEY,
  repo_id UUID NOT NULL,        -- soft ref to repositories.repositories(id)
  number BIGINT NOT NULL,
  title TEXT NOT NULL,
  body TEXT,
  author_id UUID NOT NULL,      -- soft ref to identity (human or agent)
  author_type TEXT NOT NULL,
  base_branch TEXT NOT NULL,
  head_branch TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  composite_score NUMERIC(5,4),
  score_updated_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (repo_id, number),
  CONSTRAINT chk_pull_requests_author_type_valid
    CHECK (author_type IN ('human', 'agent')),
  CONSTRAINT chk_pull_requests_status_valid
    CHECK (status IN ('open', 'closed', 'merged')),
  CONSTRAINT chk_pull_requests_composite_score_range
    CHECK (composite_score IS NULL OR (composite_score >= 0 AND composite_score <= 1))
);

CREATE INDEX idx_pull_requests_repo_status
  ON collaboration.pull_requests (repo_id, status);

CREATE TABLE collaboration.pr_reviews (
  id UUID PRIMARY KEY,
  pr_id UUID NOT NULL REFERENCES collaboration.pull_requests(id),
  reviewer_id UUID NOT NULL,    -- soft ref to identity
  reviewer_type TEXT NOT NULL,
  verdict TEXT NOT NULL,
  body TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_pr_reviews_reviewer_type_valid
    CHECK (reviewer_type IN ('human', 'agent')),
  CONSTRAINT chk_pr_reviews_verdict_valid
    CHECK (verdict IN ('approved', 'changes_requested', 'commented'))
);

CREATE TABLE collaboration.issues (
  id UUID PRIMARY KEY,
  repo_id UUID NOT NULL,        -- soft ref to repositories.repositories(id)
  number BIGINT NOT NULL,
  title TEXT NOT NULL,
  body TEXT,
  author_id UUID NOT NULL,      -- soft ref to identity
  author_type TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  composite_score NUMERIC(5,4),
  score_updated_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (repo_id, number),
  CONSTRAINT chk_issues_author_type_valid
    CHECK (author_type IN ('human', 'agent')),
  CONSTRAINT chk_issues_status_valid
    CHECK (status IN ('open', 'closed')),
  CONSTRAINT chk_issues_composite_score_range
    CHECK (composite_score IS NULL OR (composite_score >= 0 AND composite_score <= 1))
);

CREATE INDEX idx_issues_repo_status
  ON collaboration.issues (repo_id, status);

CREATE TABLE collaboration.issue_labels (
  issue_id UUID NOT NULL REFERENCES collaboration.issues(id),
  label TEXT NOT NULL,
  PRIMARY KEY (issue_id, label)
);

CREATE TABLE collaboration.pr_labels (
  pr_id UUID NOT NULL REFERENCES collaboration.pull_requests(id),
  label TEXT NOT NULL,
  PRIMARY KEY (pr_id, label)
);

-- Polymorphic parent: parent_type ('pr'|'issue') + parent_id.
-- No FK by design — pull_requests and issues are hash-partitioned (see pg/ overlay),
-- and FK from an unpartitioned child to a partitioned parent works (PG12+) but adds
-- enforcement cost on every insert. Application plane enforces parent existence.
CREATE TABLE collaboration.comments (
  id UUID PRIMARY KEY,
  parent_type TEXT NOT NULL,
  parent_id UUID NOT NULL,
  author_id UUID NOT NULL,      -- soft ref to identity
  author_type TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_comments_parent_type_valid
    CHECK (parent_type IN ('pr', 'issue')),
  CONSTRAINT chk_comments_author_type_valid
    CHECK (author_type IN ('human', 'agent'))
);

CREATE INDEX idx_comments_parent
  ON collaboration.comments (parent_type, parent_id);

CREATE TABLE collaboration.collaboration_outbox (
  id BIGSERIAL PRIMARY KEY,
  event_id UUID NOT NULL UNIQUE,
  aggregate_type TEXT NOT NULL,
  aggregate_id UUID NOT NULL,
  event_type TEXT NOT NULL,
  payload JSONB NOT NULL,
  processed_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_collaboration_outbox_unprocessed
  ON collaboration.collaboration_outbox (created_at)
  WHERE processed_at IS NULL;
