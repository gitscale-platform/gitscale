package resolvers

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/middleware"
	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/google/uuid"
)

// resolveMutationRoot dispatches the top-level Mutation fields. Each
// mutation forwards to a domain service whose backing implementation
// writes source row + outbox in the same Tx (ADR-008). GraphQL itself
// never opens a transaction.
//
// The two admin actions (createAgent, updateAgentPermissions) require an
// SVID re-verification per ADR-010 §admin actions.
func (e *Executor) resolveMutationRoot(ctx context.Context, f cost.Field, doc *cost.Document) (any, error) {
	p := principalFromCtx(ctx)
	if p.Kind == middleware.PrincipalUnknown {
		return nil, errors.New("UNAUTHENTICATED")
	}

	switch f.Name {
	case "createAgent":
		return e.resolveCreateAgent(ctx, p.ID, f)
	case "updateAgentPermissions":
		return e.resolveUpdateAgentPermissions(ctx, f)
	case "createPullRequest":
		return e.resolveCreatePullRequest(ctx, p.ID, f)
	default:
		return nil, fmt.Errorf("FIELD_NOT_SUPPORTED: Mutation.%s", f.Name)
	}
}

func (e *Executor) resolveCreateAgent(ctx context.Context, actor uuid.UUID, f cost.Field) (any, error) {
	if e.Deps.SVID != nil {
		if err := e.Deps.SVID.ReVerify(ctx); err != nil {
			return nil, ErrSVIDReVerifyFailed
		}
	}
	in, err := e.argInputObject(f.Args, "input")
	if err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	parentStr, _ := in["parentUserId"].(string)
	displayName, _ := in["displayName"].(string)
	scopeAny, _ := in["permissionScope"].([]any)
	scope := make([]string, 0, len(scopeAny))
	for _, s := range scopeAny {
		if str, ok := s.(string); ok {
			scope = append(scope, str)
		}
	}
	parentID, err := uuid.Parse(parentStr)
	if err != nil {
		return nil, fmt.Errorf("invalid parentUserId: %w", err)
	}
	// Authorisation: a human can create agents only under their own
	// parent; an agent is forbidden from creating a sibling.
	princ := principalFromCtx(ctx)
	if princ.Kind == middleware.PrincipalUnknown {
		return nil, errors.New("UNAUTHENTICATED")
	}
	if princ.Kind == middleware.PrincipalAgent {
		return nil, errors.New("FORBIDDEN: agents cannot create agents")
	}
	if parentID != actor {
		return nil, errors.New("FORBIDDEN: parentUserId must match the authenticated principal")
	}

	a, err := e.Deps.Identity.CreateAgent(ctx, parentID, displayName, scope)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"agent": map[string]any{
			"id":              a.ID.String(),
			"displayName":     a.DisplayName,
			"parentUserId":    a.ParentUserID.String(),
			"permissionScope": stringSliceToAny(a.PermissionScope),
		},
	}, nil
}

func (e *Executor) resolveUpdateAgentPermissions(ctx context.Context, f cost.Field) (any, error) {
	if e.Deps.SVID != nil {
		if err := e.Deps.SVID.ReVerify(ctx); err != nil {
			return nil, ErrSVIDReVerifyFailed
		}
	}
	in, err := e.argInputObject(f.Args, "input")
	if err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	idStr, _ := in["agentId"].(string)
	scopeAny, _ := in["permissionScope"].([]any)
	scope := make([]string, 0, len(scopeAny))
	for _, s := range scopeAny {
		if str, ok := s.(string); ok {
			scope = append(scope, str)
		}
	}
	agentID, err := uuid.Parse(idStr)
	if err != nil {
		return nil, fmt.Errorf("invalid agentId: %w", err)
	}
	if err := e.Deps.Identity.UpdateAgentPermissions(ctx, agentID, scope); err != nil {
		if errors.Is(err, identity.ErrNotImplemented) {
			return nil, err
		}
		return nil, err
	}
	return map[string]any{
		"agent": map[string]any{
			"id":              agentID.String(),
			"permissionScope": stringSliceToAny(scope),
		},
	}, nil
}

func (e *Executor) resolveCreatePullRequest(ctx context.Context, actor uuid.UUID, f cost.Field) (any, error) {
	if e.Deps.PullRequests == nil {
		return nil, ErrPullRequestsServiceUnavailable
	}
	in, err := e.argInputObject(f.Args, "input")
	if err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	repoStr, _ := in["repositoryId"].(string)
	repoID, err := uuid.Parse(repoStr)
	if err != nil {
		return nil, fmt.Errorf("invalid repositoryId: %w", err)
	}
	args := CreatePullRequestArgs{
		RepositoryID: repoID,
		BaseRef:      asString(in["baseRef"]),
		HeadRef:      asString(in["headRef"]),
		Title:        asString(in["title"]),
		Body:         asString(in["body"]),
		ActorID:      actor,
	}
	pr, err := e.Deps.PullRequests.CreatePullRequest(ctx, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"pullRequest": map[string]any{
			"id":     pr.ID.String(),
			"number": pr.Number,
			"title":  pr.Title,
			"state":  pr.State,
		},
	}, nil
}

func stringSliceToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
