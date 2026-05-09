// Package middleware contains the GraphQL HTTP middleware: principal
// resolution (adapter onto restapi.PrincipalResolver), cost metering, and
// follower-read pool selection.
//
// Each piece is decoupled from net/http where possible — the cost meter
// and follower-read selector both take a Principal + Cost and return a
// decision — so router-level glue is the only HTTP-aware code.
package middleware

import (
	"context"

	"github.com/google/uuid"
)

// PrincipalKind discriminates the authenticated caller class.
type PrincipalKind int

const (
	PrincipalUnknown PrincipalKind = iota
	PrincipalHuman
	PrincipalAgent
)

// Principal is the closed sum-type representing the authenticated caller
// in the GraphQL plane. Mirrors restapi.Principal but is duplicated here
// so the cost meter and follower-read selector don't drag the entire HTTP
// surface.
type Principal struct {
	Kind         PrincipalKind
	ID           uuid.UUID
	ParentUserID uuid.UUID // populated for agent
}

type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota
	ctxKeyRequestID
)

// WithPrincipal returns a derived ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

// PrincipalFrom returns the principal stamped on ctx, or zero value when
// absent. Callers that require authentication check Kind != PrincipalUnknown.
func PrincipalFrom(ctx context.Context) Principal {
	if v := ctx.Value(ctxKeyPrincipal); v != nil {
		if p, ok := v.(Principal); ok {
			return p
		}
	}
	return Principal{}
}

// WithRequestID stamps a request id onto ctx.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestIDFrom returns the request id stamped on ctx.
func RequestIDFrom(ctx context.Context) string {
	if v := ctx.Value(ctxKeyRequestID); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
