-- requires PostgreSQL 16+
-- Source-of-record for completed monthly partition archives (issue #74).
-- Paired with billing.billing_outbox row written in the same Tx by
-- plane/application/billing.PostgresService (ADR-008 outbox pattern).
--
-- Idempotency: UNIQUE(year, month, partition_name) anchors retry semantics
-- under unbounded Temporal activity retry; the outbox event is only emitted
-- on the first successful insert.

BEGIN;

CREATE TABLE billing.partition_archives (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  year            SMALLINT NOT NULL CHECK (year BETWEEN 2026 AND 2100),
  month           SMALLINT NOT NULL CHECK (month BETWEEN 1 AND 12),
  partition_name  TEXT     NOT NULL,
  lake_uri        TEXT     NOT NULL,
  row_count       BIGINT   NOT NULL CHECK (row_count >= 0),
  bytes_written   BIGINT   NOT NULL CHECK (bytes_written >= 0),
  archived_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (year, month, partition_name)
);

COMMIT;
