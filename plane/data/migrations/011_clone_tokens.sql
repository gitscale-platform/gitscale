-- requires PostgreSQL 16+
-- Clone-token storage for the MCP `git_clone` tool (#112).
-- Source row + identity.clone_token_minted outbox row written in the same
-- Tx (ADR-008). Tokens are short-lived (TTL ≤ 15m); the table is swept
-- by an outbox-driven consumer (follow-up issue) once expired.

CREATE TABLE IF NOT EXISTS identity.clone_tokens (
  id           UUID PRIMARY KEY,
  token        TEXT NOT NULL UNIQUE,
  principal_id UUID NOT NULL,
  repo_id      UUID NOT NULL,
  expires_at   TIMESTAMPTZ NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Hot path: lookup by token (auth) and reverse-lookup by principal (mass
-- revocation when an agent is revoked). The token UNIQUE index serves the
-- first; we add a secondary index for the second.
CREATE INDEX IF NOT EXISTS idx_clone_tokens_principal_id
  ON identity.clone_tokens (principal_id);

-- Sweep index: the cleanup job scans rows with expires_at < now().
CREATE INDEX IF NOT EXISTS idx_clone_tokens_expires_at
  ON identity.clone_tokens (expires_at);
