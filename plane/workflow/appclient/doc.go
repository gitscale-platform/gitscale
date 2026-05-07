// Package appclient holds the gRPC client interfaces by which workflow-plane
// activities call into application-plane domain services to perform state
// mutations (ADR-019).
//
// Workflow activities MUST NOT call MetadataStore.Transact for domain writes
// directly. Instead, the activity holds a per-domain client (e.g.
// IdentityClient) defined in this package, and the application plane's
// gRPC server (cmd/identity-service, ships in #15-revocation) is the only
// writer to the domain's outbox. This keeps domain invariants
// (uniqueness, clamping, event-type selection) in one codebase, lets the
// app plane stamp uniform actor_id/principal_kind/rate_bucket on every
// outbox payload from the JWT-SVID claims (ADR-010), and preserves the
// single-writer-per-aggregate property that keeps schema migrations sane.
//
// Exceptions: read-only activities MAY use plane/data/store and
// plane/data/cache interfaces directly; pure-DDL maintenance activities
// (e.g. CreatePartition in #18-rollover) MAY use MetadataStore directly
// because they emit no outbox row and have no domain invariant.
//
// This package ships interfaces only. Concrete gRPC client impls land
// alongside their server-side counterparts (e.g. identity_grpc.go in
// #15-revocation).
package appclient
