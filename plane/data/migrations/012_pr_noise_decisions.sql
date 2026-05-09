-- requires PostgreSQL 16+
-- 012: collaboration.pr_noise_decisions — primary store for the PR noise
-- pipeline's terminal decision (issue #116, ADR-008 outbox + ADR-016 dedup).
--
-- One row per pr_id; re-scoring upserts (ON CONFLICT (pr_id) DO UPDATE).
-- Downstream consumers (webhook delivery, audit) anchor idempotency on
-- the outbox event_id, NOT on pr_id — see ADR-008.
--
-- This table is intentionally NOT partitioned: the row count is bounded
-- by total PR count (1:1) and dwarfs neither pull_requests itself nor
-- collaboration_outbox; partitioning yields no benefit until we cross
-- ~10^9 rows.

CREATE TABLE IF NOT EXISTS collaboration.pr_noise_decisions (
  pr_id            UUID PRIMARY KEY,
  repo_id          UUID NOT NULL,
  org_id           UUID NOT NULL,
  agent_id         UUID,
  dedup_score      DOUBLE PRECISION NOT NULL,
  duplicate_of     UUID,
  quality_score    DOUBLE PRECISION NOT NULL,
  reputation_score DOUBLE PRECISION NOT NULL,
  composite_score  DOUBLE PRECISION NOT NULL,
  decision_code    TEXT NOT NULL,
  reason           TEXT NOT NULL,
  decided_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_pr_noise_decisions_dedup_score_range
    CHECK (dedup_score BETWEEN 0 AND 1),
  CONSTRAINT chk_pr_noise_decisions_quality_score_range
    CHECK (quality_score BETWEEN 0 AND 1),
  CONSTRAINT chk_pr_noise_decisions_reputation_score_range
    CHECK (reputation_score BETWEEN 0 AND 1),
  CONSTRAINT chk_pr_noise_decisions_composite_score_range
    CHECK (composite_score BETWEEN 0 AND 1),
  CONSTRAINT chk_pr_noise_decisions_decision_code_valid
    CHECK (decision_code IN ('auto_merge_eligible', 'maintainer_review', 'reject'))
);

-- Per-repo time-descending index for the maintainer-review queue scan
-- and the design-partner precision/recall harness.
CREATE INDEX IF NOT EXISTS idx_pr_noise_decisions_repo_time
  ON collaboration.pr_noise_decisions (repo_id, decided_at DESC);
