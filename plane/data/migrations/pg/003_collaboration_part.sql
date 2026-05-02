-- requires PostgreSQL 16+ (PG-only overlay; not applied for CRDB)
-- Converts pull_requests and issues to hash-partitioned tables (8 partitions on repo_id).
-- The UNIQUE (repo_id, number) constraint already includes the partition key, satisfying
-- PG's partition-key-inclusion rule for unique constraints on partitioned tables.

-- Approach: drop and recreate each table as PARTITION BY HASH parent, then create 8
-- partitions. Apply this overlay BEFORE any seed data is loaded — there is no in-place
-- conversion from a regular table to a partitioned table.

DROP TABLE collaboration.pull_requests CASCADE;
DROP TABLE collaboration.issues CASCADE;

CREATE TABLE collaboration.pull_requests (
  id UUID NOT NULL,
  repo_id UUID NOT NULL,
  number BIGINT NOT NULL,
  title TEXT NOT NULL,
  body TEXT,
  author_id UUID NOT NULL,
  author_type TEXT NOT NULL,
  base_branch TEXT NOT NULL,
  head_branch TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  composite_score NUMERIC(5,4),
  score_updated_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (id, repo_id),
  UNIQUE (repo_id, number),
  CONSTRAINT chk_pull_requests_author_type_valid
    CHECK (author_type IN ('human', 'agent')),
  CONSTRAINT chk_pull_requests_status_valid
    CHECK (status IN ('open', 'closed', 'merged')),
  CONSTRAINT chk_pull_requests_composite_score_range
    CHECK (composite_score IS NULL OR (composite_score >= 0 AND composite_score <= 1))
) PARTITION BY HASH (repo_id);

CREATE TABLE collaboration.pull_requests_p0 PARTITION OF collaboration.pull_requests FOR VALUES WITH (modulus 8, remainder 0);
CREATE TABLE collaboration.pull_requests_p1 PARTITION OF collaboration.pull_requests FOR VALUES WITH (modulus 8, remainder 1);
CREATE TABLE collaboration.pull_requests_p2 PARTITION OF collaboration.pull_requests FOR VALUES WITH (modulus 8, remainder 2);
CREATE TABLE collaboration.pull_requests_p3 PARTITION OF collaboration.pull_requests FOR VALUES WITH (modulus 8, remainder 3);
CREATE TABLE collaboration.pull_requests_p4 PARTITION OF collaboration.pull_requests FOR VALUES WITH (modulus 8, remainder 4);
CREATE TABLE collaboration.pull_requests_p5 PARTITION OF collaboration.pull_requests FOR VALUES WITH (modulus 8, remainder 5);
CREATE TABLE collaboration.pull_requests_p6 PARTITION OF collaboration.pull_requests FOR VALUES WITH (modulus 8, remainder 6);
CREATE TABLE collaboration.pull_requests_p7 PARTITION OF collaboration.pull_requests FOR VALUES WITH (modulus 8, remainder 7);

CREATE INDEX idx_pull_requests_repo_status
  ON collaboration.pull_requests (repo_id, status);

CREATE TABLE collaboration.issues (
  id UUID NOT NULL,
  repo_id UUID NOT NULL,
  number BIGINT NOT NULL,
  title TEXT NOT NULL,
  body TEXT,
  author_id UUID NOT NULL,
  author_type TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  composite_score NUMERIC(5,4),
  score_updated_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (id, repo_id),
  UNIQUE (repo_id, number),
  CONSTRAINT chk_issues_author_type_valid
    CHECK (author_type IN ('human', 'agent')),
  CONSTRAINT chk_issues_status_valid
    CHECK (status IN ('open', 'closed')),
  CONSTRAINT chk_issues_composite_score_range
    CHECK (composite_score IS NULL OR (composite_score >= 0 AND composite_score <= 1))
) PARTITION BY HASH (repo_id);

CREATE TABLE collaboration.issues_p0 PARTITION OF collaboration.issues FOR VALUES WITH (modulus 8, remainder 0);
CREATE TABLE collaboration.issues_p1 PARTITION OF collaboration.issues FOR VALUES WITH (modulus 8, remainder 1);
CREATE TABLE collaboration.issues_p2 PARTITION OF collaboration.issues FOR VALUES WITH (modulus 8, remainder 2);
CREATE TABLE collaboration.issues_p3 PARTITION OF collaboration.issues FOR VALUES WITH (modulus 8, remainder 3);
CREATE TABLE collaboration.issues_p4 PARTITION OF collaboration.issues FOR VALUES WITH (modulus 8, remainder 4);
CREATE TABLE collaboration.issues_p5 PARTITION OF collaboration.issues FOR VALUES WITH (modulus 8, remainder 5);
CREATE TABLE collaboration.issues_p6 PARTITION OF collaboration.issues FOR VALUES WITH (modulus 8, remainder 6);
CREATE TABLE collaboration.issues_p7 PARTITION OF collaboration.issues FOR VALUES WITH (modulus 8, remainder 7);

CREATE INDEX idx_issues_repo_status
  ON collaboration.issues (repo_id, status);
