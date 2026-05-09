package resolvers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// resolveQueryRoot dispatches the top-level Query fields. Each helper
// reads the request-scoped MetadataStore via storeFromCtx, never imports
// pgx, and returns either a materialised `map[string]any` matching the
// SDL or a typed error for the router to map.
func (e *Executor) resolveQueryRoot(ctx context.Context, f cost.Field, doc *cost.Document) (any, error) {
	switch f.Name {
	case "user":
		login, _ := e.argString(f.Args, "login")
		return e.resolveUser(ctx, login, f, doc)
	case "agent":
		idArg, _ := e.argString(f.Args, "id")
		return e.resolveAgent(ctx, idArg, f, doc)
	case "repository":
		owner, _ := e.argString(f.Args, "owner")
		name, _ := e.argString(f.Args, "name")
		return e.resolveRepository(ctx, owner, name, f, doc)
	case "organization":
		login, _ := e.argString(f.Args, "login")
		return e.resolveOrganization(ctx, login, f, doc)
	case "pullRequest":
		idArg, _ := e.argString(f.Args, "id")
		return e.resolvePullRequest(ctx, idArg, f, doc)
	default:
		return nil, fmt.Errorf("FIELD_NOT_SUPPORTED: Query.%s", f.Name)
	}
}

func (e *Executor) resolveUser(ctx context.Context, login string, f cost.Field, doc *cost.Document) (any, error) {
	if login == "" {
		return nil, errors.New("login is required")
	}
	mds := storeFromCtx(ctx)
	if mds == nil {
		return nil, errors.New("internal: no MetadataStore in context")
	}

	// Login is GitScale-specific; we treat it as the user's email-prefix
	// for the purposes of the field-stable surface. A full username
	// surface lands with #15-revocation.
	user, err := lookupUserByLogin(ctx, mds, login)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, identity.ErrUserNotFound
	}
	return projectUser(user, f, doc), nil
}

func (e *Executor) resolveAgent(ctx context.Context, idArg string, f cost.Field, doc *cost.Document) (any, error) {
	mds := storeFromCtx(ctx)
	if mds == nil {
		return nil, errors.New("internal: no MetadataStore in context")
	}
	agentID, err := uuid.Parse(idArg)
	if err != nil {
		return nil, fmt.Errorf("invalid agent id: %w", err)
	}
	agent, err := mds.Identity().GetAgentByID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, identity.ErrAgentNotFound
	}
	return projectAgent(agent, f, doc), nil
}

func (e *Executor) resolveRepository(ctx context.Context, owner, name string, f cost.Field, doc *cost.Document) (any, error) {
	mds := storeFromCtx(ctx)
	if mds == nil {
		return nil, errors.New("internal: no MetadataStore in context")
	}
	if owner == "" || name == "" {
		return nil, errors.New("owner and name are required")
	}
	// We have RepositoryReader.GetBySlug; "name" maps to slug.
	repo, err := mds.Repositories().GetBySlug(ctx, name)
	if err != nil {
		return nil, err
	}
	if repo == nil {
		return nil, repositories.ErrRepositoryNotFound
	}
	return projectRepository(ctx, mds, repo, f, doc), nil
}

func (e *Executor) resolveOrganization(_ context.Context, login string, _ cost.Field, _ *cost.Document) (any, error) {
	if login == "" {
		return nil, errors.New("login is required")
	}
	// Organisations table is owned by the identity domain; we project a
	// shallow view (id + login + members=empty) until #15-revocation
	// surfaces a richer org reader. Field-stable: the field NAMES are
	// available; downstream agents that depend on `members` will see an
	// empty connection until the domain reader lands.
	return map[string]any{
		"id":    login,
		"login": login,
		"members": map[string]any{
			"nodes":      []any{},
			"totalCount": int32(0),
			"pageInfo":   map[string]any{"endCursor": nil, "hasNextPage": false},
		},
	}, nil
}

func (e *Executor) resolvePullRequest(_ context.Context, _ string, _ cost.Field, _ *cost.Document) (any, error) {
	// pullrequests.Service is not yet wired; the resolver returns NULL
	// (nil) for now with a NOT_IMPLEMENTED-class error. Mirrors the
	// CreatePullRequest mutation pattern.
	return nil, ErrPullRequestsServiceUnavailable
}

// ErrPullRequestsServiceUnavailable is returned by both the query and
// mutation resolvers when Deps.PullRequests is nil.
var ErrPullRequestsServiceUnavailable = errors.New("graphql: pullrequests.Service not yet wired")

// lookupUserByLogin maps a GraphQL `login` (GitScale interpretation:
// email) to a HumanUser via store.IdentityReader. The full login surface
// lands with #15-revocation.
func lookupUserByLogin(ctx context.Context, mds store.MetadataStore, login string) (*store.HumanUser, error) {
	// Treat "login" as either an email or a UUID-shaped principal id.
	if u, err := uuid.Parse(login); err == nil {
		return mds.Identity().GetUserByID(ctx, u)
	}
	return mds.Identity().GetUserByEmail(ctx, login)
}

// projectUser materialises the requested fields of a HumanUser into the
// JSON-able output map. Sub-fields not present in the selection set are
// omitted from the response — minimum-information principle.
func projectUser(u *store.HumanUser, f cost.Field, _ *cost.Document) map[string]any {
	out := map[string]any{}
	for _, sel := range f.Sels {
		if sel.Kind != cost.SelField {
			continue
		}
		switch sel.Field.Name {
		case "id":
			out["id"] = u.ID.String()
		case "login":
			out["login"] = u.Email
		case "name":
			out["name"] = u.Email
		case "email":
			out["email"] = u.Email
		case "createdAt":
			out["createdAt"] = u.CreatedAt.Format(time.RFC3339Nano)
		case "__typename":
			out["__typename"] = "User"
		}
	}
	return out
}

func projectAgent(a *store.AgentIdentity, f cost.Field, _ *cost.Document) map[string]any {
	out := map[string]any{}
	for _, sel := range f.Sels {
		if sel.Kind != cost.SelField {
			continue
		}
		switch sel.Field.Name {
		case "id":
			out["id"] = a.ID.String()
		case "displayName":
			out["displayName"] = a.DisplayName
		case "parentUserId":
			out["parentUserId"] = a.ParentUserID.String()
		case "permissionScope":
			scope := make([]any, len(a.PermissionScope))
			for i, s := range a.PermissionScope {
				scope[i] = s
			}
			out["permissionScope"] = scope
		case "reputationScore":
			out["reputationScore"] = a.ReputationScore
		case "createdAt":
			out["createdAt"] = a.CreatedAt.Format(time.RFC3339Nano)
		case "__typename":
			out["__typename"] = "Agent"
		}
	}
	return out
}

func projectRepository(_ context.Context, _ store.MetadataStore, r *store.Repository, f cost.Field, _ *cost.Document) map[string]any {
	out := map[string]any{}
	for _, sel := range f.Sels {
		if sel.Kind != cost.SelField {
			continue
		}
		switch sel.Field.Name {
		case "id":
			out["id"] = r.ID.String()
		case "name":
			out["name"] = r.Name
		case "nameWithOwner":
			out["nameWithOwner"] = r.OrgID.String() + "/" + r.Slug
		case "visibility":
			out["visibility"] = r.Visibility
		case "createdAt":
			out["createdAt"] = r.CreatedAt.Format(time.RFC3339Nano)
		case "owner":
			// Owner projection is shallow: id + login mapped from owner_id.
			out["owner"] = map[string]any{
				"id":    r.OwnerID.String(),
				"login": r.OwnerID.String(),
			}
		case "defaultBranch":
			if r.DefaultBranch == "" {
				out["defaultBranch"] = nil
			} else {
				out["defaultBranch"] = map[string]any{
					"name":   r.DefaultBranch,
					"prefix": "refs/heads/",
				}
			}
		case "pullRequests":
			// PR connection is empty until pullrequests.Service is wired.
			out["pullRequests"] = emptyConnection()
		case "issues":
			out["issues"] = emptyConnection()
		case "__typename":
			out["__typename"] = "Repository"
		}
	}
	return out
}

func emptyConnection() map[string]any {
	return map[string]any{
		"nodes":      []any{},
		"totalCount": int32(0),
		"pageInfo":   map[string]any{"endCursor": nil, "hasNextPage": false},
	}
}
