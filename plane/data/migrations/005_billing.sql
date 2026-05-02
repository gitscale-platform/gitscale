-- requires PostgreSQL 16+
-- Billing domain: quota accounts, quota windows, usage events (range-partitioned),
-- invoices, outbox. surface_kind ENUM is the single source of truth for billable surfaces.

CREATE SCHEMA IF NOT EXISTS billing;

-- Single-source-of-truth enum for billable surfaces; both quota_windows and usage_events
-- use this type. Add new surfaces with ALTER TYPE billing.surface_kind ADD VALUE '<name>'.
CREATE TYPE billing.surface_kind AS ENUM (
  'tokens',
  'compute_minutes',
  'storage_gb',
  'api_requests'
);

CREATE TABLE billing.quota_accounts (
  id UUID PRIMARY KEY,
  org_id UUID NOT NULL UNIQUE,  -- soft ref to identity.organisations(id)
  plan_tier TEXT NOT NULL DEFAULT 'free',
  tokens_per_week_cap BIGINT NOT NULL DEFAULT 1000000,
  compute_minutes_per_month_cap BIGINT NOT NULL DEFAULT 500,
  storage_gb_cap BIGINT NOT NULL DEFAULT 10,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_quota_accounts_plan_tier_valid
    CHECK (plan_tier IN ('free', 'pro', 'enterprise'))
);

-- quota_windows.cap is materialised at window creation by copying from the relevant
-- quota_accounts.*_cap. It is NOT updated retroactively — mid-period plan changes take
-- effect at the next window boundary. This decouples open-window enforcement from plan
-- mutations (ADR-012 two-tier counter model).
CREATE TABLE billing.quota_windows (
  id UUID PRIMARY KEY,
  account_id UUID NOT NULL REFERENCES billing.quota_accounts(id),
  surface billing.surface_kind NOT NULL,
  window_start TIMESTAMPTZ NOT NULL,
  window_end TIMESTAMPTZ NOT NULL,
  cap BIGINT NOT NULL,
  consumed BIGINT NOT NULL DEFAULT 0,
  UNIQUE (account_id, surface, window_start),
  CONSTRAINT chk_quota_windows_window_order
    CHECK (window_end > window_start),
  CONSTRAINT chk_quota_windows_cap_nonnegative
    CHECK (cap >= 0),
  CONSTRAINT chk_quota_windows_consumed_nonnegative
    CHECK (consumed >= 0)
);

CREATE INDEX idx_quota_windows_account_surface_start
  ON billing.quota_windows (account_id, surface, window_start);

-- usage_events: high-volume append-only, range-partitioned by ts (monthly).
-- Initial partitions created below cover current month + 11 months ahead.
-- A Temporal monthly cron workflow rolls forward (creates next month) and
-- archives/drops old partitions per retention policy. (Filed as separate issue
-- under plane/workflow/.)
CREATE TABLE billing.usage_events (
  id UUID NOT NULL,
  account_id UUID NOT NULL REFERENCES billing.quota_accounts(id),
  principal_id UUID NOT NULL,           -- soft ref to identity (human/agent/ci_runner)
  principal_type TEXT NOT NULL,
  surface billing.surface_kind NOT NULL,
  cost_vector JSONB NOT NULL DEFAULT '{}'::jsonb,
  -- cost_vector documented payload (extensible):
  --   class:  subdivides surface (e.g., 'gpu', 'cpu', 'hot', 'cold')
  --   tier:   'hot' | 'cold' for storage/compute
  --   model:  for token surfaces (e.g., 'claude-sonnet-4.6')
  --   tags:   array of free-form cost-allocation tags
  value BIGINT NOT NULL,
  repo_id UUID,                         -- soft ref to repositories.repositories(id), nullable
  event_source TEXT NOT NULL,
  external_event_id UUID UNIQUE,        -- idempotency for ClickHouse → PG sync
  ts TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (id, ts),                  -- partition key included per PG requirement
  CONSTRAINT chk_usage_events_principal_type_valid
    CHECK (principal_type IN ('human', 'agent', 'ci_runner')),
  CONSTRAINT chk_usage_events_value_nonnegative
    CHECK (value >= 0),
  CONSTRAINT chk_usage_events_cost_vector_object
    CHECK (jsonb_typeof(cost_vector) = 'object')
) PARTITION BY RANGE (ts);

CREATE INDEX idx_usage_events_account_surface_ts
  ON billing.usage_events (account_id, surface, ts);

CREATE INDEX idx_usage_events_cost_vector
  ON billing.usage_events USING GIN (cost_vector);

-- Initial 12 monthly partitions (current month + 11 ahead).
CREATE TABLE billing.usage_events_2026_05 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE billing.usage_events_2026_06 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE billing.usage_events_2026_07 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE billing.usage_events_2026_08 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE billing.usage_events_2026_09 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE billing.usage_events_2026_10 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
CREATE TABLE billing.usage_events_2026_11 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');
CREATE TABLE billing.usage_events_2026_12 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2026-12-01') TO ('2027-01-01');
CREATE TABLE billing.usage_events_2027_01 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2027-01-01') TO ('2027-02-01');
CREATE TABLE billing.usage_events_2027_02 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2027-02-01') TO ('2027-03-01');
CREATE TABLE billing.usage_events_2027_03 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2027-03-01') TO ('2027-04-01');
CREATE TABLE billing.usage_events_2027_04 PARTITION OF billing.usage_events
  FOR VALUES FROM ('2027-04-01') TO ('2027-05-01');

CREATE TABLE billing.invoices (
  id UUID PRIMARY KEY,
  account_id UUID NOT NULL REFERENCES billing.quota_accounts(id),
  period_start TIMESTAMPTZ NOT NULL,
  period_end TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft',
  line_items JSONB NOT NULL DEFAULT '[]'::jsonb,
  total_amount_cents BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_invoices_status_valid
    CHECK (status IN ('draft', 'finalized', 'paid', 'void')),
  CONSTRAINT chk_invoices_period_order
    CHECK (period_end > period_start),
  CONSTRAINT chk_invoices_line_items_array
    CHECK (jsonb_typeof(line_items) = 'array')
);

CREATE TABLE billing.billing_outbox (
  id BIGSERIAL PRIMARY KEY,
  event_id UUID NOT NULL UNIQUE,
  aggregate_type TEXT NOT NULL,
  aggregate_id UUID NOT NULL,
  event_type TEXT NOT NULL,
  payload JSONB NOT NULL,
  processed_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_billing_outbox_unprocessed
  ON billing.billing_outbox (created_at)
  WHERE processed_at IS NULL;
