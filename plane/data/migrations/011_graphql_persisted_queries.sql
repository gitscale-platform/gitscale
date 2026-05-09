-- 011_graphql_persisted_queries.sql
--
-- Adds the graphql.persisted_queries table backing the GraphQL persisted-
-- query path (issue #113, ADR-017). The hash format is "sha256:" + 64 hex
-- chars; the table key is the hash so re-registration of an identical body
-- is a no-op via ON CONFLICT (hash) DO NOTHING (cf. persisted/postgres_store.go).

CREATE SCHEMA IF NOT EXISTS graphql;

CREATE TABLE IF NOT EXISTS graphql.persisted_queries (
  hash           TEXT PRIMARY KEY,
  query          TEXT NOT NULL,
  registered_by  UUID NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE graphql.persisted_queries IS
  'GraphQL persisted-query store. The hash is the SHA-256 of the query body, prefixed with "sha256:". Read-through cached via CacheStore (TTL 24h).';
