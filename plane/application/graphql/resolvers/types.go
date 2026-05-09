// Package resolvers implements the GraphQL field resolvers for the GitScale
// schema. Resolvers are pure leaves: they read MetadataStore from the
// request context (which the follower-read middleware populates with the
// correct pool) and forward mutations to existing application-plane
// services. They never import pgx, redis, or other concrete drivers
// (ADR-017). Mutations never write outbox rows directly — the underlying
// services own that (ADR-008).
package resolvers

import (
	"context"
	"errors"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/middleware"
	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// ID is the resolver-package alias for GraphQL ID values, decoupling
// resolver code from any third-party GraphQL library.
type ID string

// PullRequestsService is the (future) gRPC client surface for pull-request
// mutations. It is declared here as an interface so the resolver can
// short-circuit to NOT_IMPLEMENTED when the dep is nil — unblocks shipping
// the GraphQL surface ahead of the pullrequests.Service landing.
type PullRequestsService interface {
	CreatePullRequest(ctx context.Context, in CreatePullRequestArgs) (*PullRequestModel, error)
}

// CreatePullRequestArgs is the wire shape the resolver hands off to the
// underlying service.
type CreatePullRequestArgs struct {
	RepositoryID uuid.UUID
	BaseRef      string
	HeadRef      string
	Title        string
	Body         string
	ActorID      uuid.UUID
}

// PullRequestModel is the minimal pull-request projection.
type PullRequestModel struct {
	ID         uuid.UUID
	Number     int32
	Title      string
	State      string
	AuthorID   uuid.UUID
	CreatedAt  time.Time
}

// SVIDReVerifier implements the ADR-010 admin-action re-verification check.
// Production: SPIRE/SPIFFE attestation; tests: a stub returning nil/error.
type SVIDReVerifier interface {
	ReVerify(ctx context.Context) error
}

// AlwaysVerifiedSVID is a SVIDReVerifier that always succeeds. Suitable
// for local dev with REST_API_INSECURE-like guarantees; production must
// inject the real SPIRE-backed verifier.
type AlwaysVerifiedSVID struct{}

// ReVerify implements SVIDReVerifier.
func (AlwaysVerifiedSVID) ReVerify(_ context.Context) error { return nil }

// ErrSVIDReVerifyFailed is the sentinel returned by mutation resolvers
// when the admin-action re-verify step fails.
var ErrSVIDReVerifyFailed = errors.New("graphql: SVID re-verify failed")

// Deps bundles resolver dependencies. The follower-read selector populates
// the per-request MetadataStore on context; resolver code reaches it via
// middleware.StoreFrom. Identity and PullRequests are domain services
// (ADR-019: GraphQL composes, never owns).
type Deps struct {
	Identity     identity.Service
	Repositories repositories.Service
	PullRequests PullRequestsService // may be nil → resolver returns NOT_IMPLEMENTED
	SVID         SVIDReVerifier
}

// storeFromCtx is a helper shared by all resolvers.
func storeFromCtx(ctx context.Context) store.MetadataStore {
	return middleware.StoreFrom(ctx)
}

// principalFromCtx resolves the authenticated principal; resolvers that
// require auth bail with UNAUTHENTICATED-mapped errors when zero-valued.
func principalFromCtx(ctx context.Context) middleware.Principal {
	return middleware.PrincipalFrom(ctx)
}

// parseUUID is a typed wrapper for ID-class GraphQL args. It centralises
// the parse rule so callers don't sprinkle string conversions.
//
//nolint:unused // exported indirectly via tests / mutation_root.
func parseUUID(v ID) (uuid.UUID, error) {
	return uuid.Parse(string(v))
}
