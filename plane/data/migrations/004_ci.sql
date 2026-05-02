-- requires PostgreSQL 16+
-- CI domain: workflow runs, jobs, runner assignments (with history), outbox.
-- runner_assignments preserves history; partial unique index enforces single-active per job.
-- ci_jobs.firecracker_vm_id was dropped in favour of single-source-of-truth in runner_assignments.

CREATE SCHEMA IF NOT EXISTS ci;

CREATE TABLE ci.workflow_runs (
  id UUID PRIMARY KEY,
  repo_id UUID NOT NULL,        -- soft ref to repositories.repositories(id)
  trigger_type TEXT NOT NULL,
  trigger_event_id UUID,
  initiator_id UUID NOT NULL,   -- soft ref to identity (human or agent)
  initiator_type TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued',
  runner_tier TEXT NOT NULL DEFAULT 'cold',
  temporal_workflow_id TEXT,    -- nullable; set after Temporal workflow created
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_workflow_runs_trigger_type_valid
    CHECK (trigger_type IN ('push', 'pr', 'manual', 'schedule')),
  CONSTRAINT chk_workflow_runs_initiator_type_valid
    CHECK (initiator_type IN ('human', 'agent')),
  CONSTRAINT chk_workflow_runs_status_valid
    CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  CONSTRAINT chk_workflow_runs_runner_tier_valid
    CHECK (runner_tier IN ('hot', 'cold'))
);

CREATE INDEX idx_workflow_runs_repo_status
  ON ci.workflow_runs (repo_id, status);

CREATE INDEX idx_workflow_runs_initiator_tier
  ON ci.workflow_runs (initiator_type, runner_tier);
-- EXPLAIN: tier distribution dashboards (human vs agent breakdown).

CREATE UNIQUE INDEX uidx_workflow_runs_temporal_workflow_id
  ON ci.workflow_runs (temporal_workflow_id)
  WHERE temporal_workflow_id IS NOT NULL;
-- Enforces 1:1 mapping between workflow_runs and Temporal workflows when set.
-- Doubles as the lookup index used by Temporal workers on every activity start.

CREATE TABLE ci.ci_jobs (
  id UUID PRIMARY KEY,
  run_id UUID NOT NULL REFERENCES ci.workflow_runs(id),
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued',
  runner_tier TEXT NOT NULL,
  -- firecracker_vm_id INTENTIONALLY OMITTED: source of truth lives in
  -- ci.runner_assignments. Query via:
  --   SELECT vm_id FROM ci.runner_assignments
  --     WHERE job_id = $1 AND released_at IS NULL;
  -- For most-recent (incl. released): ORDER BY assigned_at DESC LIMIT 1.
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_ci_jobs_status_valid
    CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  CONSTRAINT chk_ci_jobs_runner_tier_valid
    CHECK (runner_tier IN ('hot', 'cold'))
);

CREATE INDEX idx_ci_jobs_run_status
  ON ci.ci_jobs (run_id, status);

CREATE TABLE ci.runner_assignments (
  id UUID PRIMARY KEY,
  job_id UUID NOT NULL REFERENCES ci.ci_jobs(id),
  vm_id TEXT NOT NULL,
  runner_tier TEXT NOT NULL,
  assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  released_at TIMESTAMPTZ,
  CONSTRAINT chk_runner_assignments_runner_tier_valid
    CHECK (runner_tier IN ('hot', 'cold'))
);

CREATE UNIQUE INDEX uidx_runner_assignments_active
  ON ci.runner_assignments (job_id)
  WHERE released_at IS NULL;
-- Enforces single active assignment per job while preserving full history rows.
-- Re-queue creates a new row instead of overwriting; old row is retained for audit
-- (e.g., post-incident "which VM ran the failed attempt?").

CREATE TABLE ci.ci_outbox (
  id BIGSERIAL PRIMARY KEY,
  event_id UUID NOT NULL UNIQUE,
  aggregate_type TEXT NOT NULL,
  aggregate_id UUID NOT NULL,
  event_type TEXT NOT NULL,
  payload JSONB NOT NULL,
  processed_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ci_outbox_unprocessed
  ON ci.ci_outbox (created_at)
  WHERE processed_at IS NULL;

-- runner_tier default = 'cold': agents get cold pool unless require-hot-pool annotation
-- present (applied at assignment time, not schema time — ADR-002).
