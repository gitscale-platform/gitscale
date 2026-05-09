package restapi

import (
	"context"
	"errors"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// identityLookuper is the subset of store.IdentityReader the production
// resolver needs. It is a separate interface so tests can substitute a
// minimal stub without pulling identity.Service.
type identityLookuper interface {
	LookupIdentityForCache(ctx context.Context, principalID uuid.UUID) (*store.IdentityCacheEntry, error)
	GetAgentByID(ctx context.Context, id uuid.UUID) (*store.AgentIdentity, error)
}

// IdentityResolver maps a UUID-shaped bearer token to a Principal via
// store.IdentityReader. The bearer-token format is intentionally minimal in
// this iteration (raw UUID); a signed-JWT path lands with ADR-010 hardening
// in the edge plane.
type IdentityResolver struct {
	store identityLookuper
}

// NewIdentityResolver wires the production resolver onto an
// IdentityReader (typically obtained via store.MetadataStore.Identity()).
func NewIdentityResolver(reader identityLookuper) *IdentityResolver {
	return &IdentityResolver{store: reader}
}

// Resolve parses the bearer as a UUID and looks up the principal kind.
// Unknown principals → ErrInvalidToken; agent principals are decorated
// with their parent_user_id so authorisation rules in handlers can use it
// without an extra round-trip.
func (r *IdentityResolver) Resolve(ctx context.Context, bearer string) (Principal, error) {
	id, err := uuid.Parse(bearer)
	if err != nil {
		return nil, ErrInvalidToken
	}
	entry, err := r.store.LookupIdentityForCache(ctx, id)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, ErrInvalidToken
	}
	switch entry.Kind {
	case "human":
		return HumanPrincipal{UserID: entry.PrincipalID}, nil
	case "agent":
		agent, err := r.store.GetAgentByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if agent == nil {
			return nil, ErrInvalidToken
		}
		return AgentPrincipal{AgentID: agent.ID, ParentUserID: agent.ParentUserID}, nil
	default:
		return nil, errors.New("restapi: unknown principal kind " + entry.Kind)
	}
}
