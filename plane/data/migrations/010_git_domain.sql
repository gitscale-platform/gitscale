-- requires PostgreSQL 16+
-- Git domain: outbox table for metering events emitted by the Gitaly hook
-- layer (ADR-015 reconciliation tier). The git plane has no co-located
-- relational state in v1 — the outbox is the only table — but it ships
-- under its own schema so the existing per-domain outbox consumer wiring
-- (plane/data/outbox/wiring) drains it without special-casing.

CREATE SCHEMA IF NOT EXISTS git;

CREATE TABLE git.git_outbox (
  id BIGSERIAL PRIMARY KEY,
  event_id UUID NOT NULL UNIQUE,
  aggregate_type TEXT NOT NULL,
  aggregate_id UUID NOT NULL,
  event_type TEXT NOT NULL,
  payload JSONB NOT NULL,
  processed_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_git_outbox_unprocessed
  ON git.git_outbox (created_at)
  WHERE processed_at IS NULL;
