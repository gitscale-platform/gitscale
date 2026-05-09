package restapi

import (
	"context"

	"github.com/google/uuid"
)

// PrincipalKind discriminates the concrete principal type.
type PrincipalKind int

const (
	// PrincipalUnknown is the zero value; never present on an authenticated
	// request.
	PrincipalUnknown PrincipalKind = iota
	PrincipalHuman
	PrincipalAgent
)

// String renders the kind for log fields.
func (k PrincipalKind) String() string {
	switch k {
	case PrincipalHuman:
		return "human"
	case PrincipalAgent:
		return "agent"
	default:
		return "unknown"
	}
}

// Principal is the closed sum-type representing the authenticated caller.
// The concrete implementations are HumanPrincipal and AgentPrincipal.
type Principal interface {
	Kind() PrincipalKind
	ID() uuid.UUID
}

// HumanPrincipal authenticates as a human user.
type HumanPrincipal struct {
	UserID uuid.UUID
}

// Kind returns PrincipalHuman.
func (p HumanPrincipal) Kind() PrincipalKind { return PrincipalHuman }

// ID returns the human user UUID.
func (p HumanPrincipal) ID() uuid.UUID { return p.UserID }

// AgentPrincipal authenticates as an agent identity.
type AgentPrincipal struct {
	AgentID      uuid.UUID
	ParentUserID uuid.UUID
}

// Kind returns PrincipalAgent.
func (p AgentPrincipal) Kind() PrincipalKind { return PrincipalAgent }

// ID returns the agent UUID.
func (p AgentPrincipal) ID() uuid.UUID { return p.AgentID }

type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota
	ctxKeyRequestID
)

// WithPrincipal returns a derived context carrying p. Used by the auth
// middleware; handlers read via PrincipalFromContext.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

// PrincipalFromContext returns the principal stamped by the auth
// middleware, or nil if none is present.
func PrincipalFromContext(ctx context.Context) Principal {
	if v := ctx.Value(ctxKeyPrincipal); v != nil {
		if p, ok := v.(Principal); ok {
			return p
		}
	}
	return nil
}

// WithRequestID stamps a request-id onto ctx.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestIDFromContext returns the request id stamped by the request_id
// middleware, or the empty string when not set.
func RequestIDFromContext(ctx context.Context) string {
	if v := ctx.Value(ctxKeyRequestID); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
