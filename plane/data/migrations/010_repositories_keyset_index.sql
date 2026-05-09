-- Issue #111 — REST API HTTP layer.
--
-- Adds the keyset-pagination index for GET /v1/orgs/{org}/repos which
-- runs `WHERE org_id = $1 AND (created_at, id) > ($2, $3) ORDER BY
-- created_at, id LIMIT $4`. The composite (org_id, created_at, id) is
-- the minimum index that supports both the equality on org_id and the
-- row-value comparison on (created_at, id) without a sort step.

CREATE INDEX IF NOT EXISTS idx_repositories_org_keyset
  ON repositories.repositories (org_id, created_at, id);
