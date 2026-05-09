-- Issue noise filtering — spam detection + maintainer queue routing.
-- Issue: https://github.com/gitscale-platform/gitscale/issues/117
-- Spec:  docs/superpowers/specs/2026-05-09-issue-117-issue-noise-filtering-design.md
-- ADR:   ADR-008 (outbox), ADR-016 (Vespa), ADR-017 (swap surface), ADR-019 (plane boundary).
--
-- Adds:
--   1. Two new values to the collaboration.issues.status CHECK constraint:
--        'held'              — held in the maintainer review queue.
--        'auto_closed_spam'  — terminal state for spam-classified issues.
--      The existing values 'open' and 'closed' are preserved.
--   2. collaboration.issue_noise_decisions — append-only audit table. One
--      row per Router.Route call AND one row per Router.Release call.
--   3. collaboration.issue_noise_config — per-repo threshold overrides.
--      Defaults are platform-wide constants in Go; a row here only exists
--      when an operator has tuned this repo away from defaults.
--
-- Note on the CHECK constraint update: collaboration.issues uses a TEXT
-- column with a CHECK constraint (not an ENUM type), so this migration
-- drops + recreates the constraint. Safe under SERIALIZABLE isolation
-- as long as no in-flight Tx is mid-INSERT against the table at the
-- moment of ALTER. Production runs this in maintenance-window cadence
-- (per the migrations runbook).

ALTER TABLE collaboration.issues
  DROP CONSTRAINT IF EXISTS chk_issues_status_valid;

ALTER TABLE collaboration.issues
  ADD CONSTRAINT chk_issues_status_valid
  CHECK (status IN ('open', 'closed', 'held', 'auto_closed_spam'));

CREATE TABLE collaboration.issue_noise_decisions (
  decision_id      UUID PRIMARY KEY,
  issue_id         UUID NOT NULL,                  -- soft ref; issues is hash-partitioned (see pg/ overlay)
  repo_id          UUID NOT NULL,
  reporter_id      UUID NOT NULL,
  verdict          TEXT NOT NULL,
  scorer_version   TEXT NOT NULL,
  score_spam       NUMERIC(4,3) NOT NULL,
  score_low_quality NUMERIC(4,3) NOT NULL,
  score_duplicate  NUMERIC(4,3) NOT NULL,
  duplicate_of     UUID NULL,
  signals          JSONB NOT NULL DEFAULT '[]'::jsonb,
  decided_at       TIMESTAMPTZ NOT NULL,
  decided_by       TEXT NOT NULL,                  -- 'auto' | 'maintainer:<uuid>'
  CONSTRAINT chk_issue_noise_decisions_verdict_valid
    CHECK (verdict IN ('normal', 'low_quality', 'duplicate', 'spam')),
  CONSTRAINT chk_issue_noise_decisions_scores_range
    CHECK (
      score_spam        BETWEEN 0 AND 1
      AND score_low_quality BETWEEN 0 AND 1
      AND score_duplicate   BETWEEN 0 AND 1
    )
);

CREATE INDEX idx_issue_noise_decisions_issue
  ON collaboration.issue_noise_decisions (issue_id, decided_at DESC);

CREATE INDEX idx_issue_noise_decisions_repo_verdict
  ON collaboration.issue_noise_decisions (repo_id, verdict, decided_at DESC);

CREATE TABLE collaboration.issue_noise_config (
  repo_id              UUID PRIMARY KEY,
  spam_floor           NUMERIC(4,3) NOT NULL DEFAULT 0.700,
  low_quality_floor    NUMERIC(4,3) NOT NULL DEFAULT 0.400,
  duplicate_floor      NUMERIC(4,3) NOT NULL DEFAULT 0.850,
  hold_ttl_seconds     INT NOT NULL DEFAULT 1209600,    -- 14 days
  enforce              BOOLEAN NOT NULL DEFAULT FALSE,  -- dark-launch default
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_issue_noise_config_floors_range
    CHECK (
      spam_floor        BETWEEN 0 AND 1
      AND low_quality_floor BETWEEN 0 AND 1
      AND duplicate_floor   BETWEEN 0 AND 1
    ),
  CONSTRAINT chk_issue_noise_config_hold_ttl_positive
    CHECK (hold_ttl_seconds > 0)
);
