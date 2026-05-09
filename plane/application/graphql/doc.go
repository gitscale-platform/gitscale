// Package graphql is the application-plane GraphQL surface for GitScale.
// It exposes a field-stable subset of GitHub's GraphQL schema (Query roots
// repository, user, agent, pullRequest, organization) and three mutations
// (createPullRequest, createAgent, updateAgentPermissions).
//
// ADR-017 (interface swap surface) governs every storage touch in this
// package:
//
//	"Three Go interfaces are defined in plane/data/: MetadataStore (all SQL
//	 operations across the five schema domains), CacheStore (key-value,
//	 pub/sub, and TTL semantics needed by the edge and Git proxy), and
//	 EventQueue (outbox-to-Kafka publishing). Application code never imports
//	 a concrete driver; it receives a concrete implementation injected at
//	 startup."
//
// Resolvers receive store.MetadataStore and cache.CacheStore via the package
// Deps struct; they never import pgx or redis. The follower-read default is
// implemented by injecting two MetadataStore instances (Reader replica +
// Primary), with @liveRead and all mutations routing to Primary.
//
// Plane boundary (ADR-019): GraphQL is purely application-plane. It must
// not import plane/git, plane/workflow, or plane/edge. Mutations forward to
// existing application-plane services (identity.Service, future
// pullrequests.Service) that own outbox correctness (ADR-008); GraphQL
// writes no outbox rows directly.
//
// Cost analysis (depth + complexity + agent-class multiplier) runs before
// any resolver executes. Rejected queries pay parse_cost against the
// per-principal "graphql" rate-limit bucket so probe-floods are not free.
//
// Phase 1 ships behind GRAPHQL_PREVIEW=true; Phase 2 GA gating is defined
// in docs/slo/graphql.md.
package graphql
