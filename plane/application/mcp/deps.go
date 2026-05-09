package mcp

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gitscale-platform/gitscale/plane/application/agentsmd/hook"
	"github.com/gitscale-platform/gitscale/plane/application/agentsmd/policystore"
	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/mcp/cirunclient"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
)

// CloneURLBuilder turns a (repo, principal, token) tuple into the
// HTTPS URL the agent should clone from. Carved out so the URL shape
// is configurable per-deployment without touching the MCP package.
type CloneURLBuilder interface {
	BuildCloneURL(ctx context.Context, repo *repositories.Repository, token string) (string, error)
}

// CloneURLBuilderFunc adapts a function to CloneURLBuilder.
type CloneURLBuilderFunc func(ctx context.Context, repo *repositories.Repository, token string) (string, error)

// BuildCloneURL implements CloneURLBuilder.
func (f CloneURLBuilderFunc) BuildCloneURL(ctx context.Context, repo *repositories.Repository, token string) (string, error) {
	return f(ctx, repo, token)
}

// Deps bundles the inputs to NewServer. All fields except Logger and
// CloneURLBuilder are required; NewServer returns an error if a
// required field is nil.
//
// RESTHandler is the http.Handler returned by restapi.NewRouter; the
// MCP layer dispatches REST-backed tools through this handler via the
// in-process loopback (no TCP round-trip). Reusing the REST middleware
// chain is intentional (ADR-019) — auth + error mapping live in one
// place.
type Deps struct {
	Identity        identity.Service
	Repositories    repositories.Service
	Resolver        restapi.PrincipalResolver
	Limiter         ratelimit.RateLimiter
	BlobReader      hook.BlobReader
	OrgPolicy       policystore.RepoMetadata
	CIRunClient     cirunclient.Client
	RESTHandler     http.Handler
	CloneURLBuilder CloneURLBuilder
	Logger          *slog.Logger
}
