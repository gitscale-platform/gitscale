package mcp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
)

// TestMapErr_ExhaustiveSentinels verifies that every sentinel the MCP
// layer maps lands on the expected JSON-RPC code. Adding a new
// sentinel without extending mapErr will fail the default-arm test
// below (the unhandled error returns CodeInternal).
func TestMapErr_ExhaustiveSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Code
	}{
		{"nil", nil, CodeInternal},

		{"mcp/not_implemented", ErrNotImplemented, CodeNotImplemented},
		{"mcp/forbidden", ErrForbidden, CodeForbidden},

		{"identity/invalid_email", identity.ErrInvalidEmail, CodeInvalidParams},
		{"identity/empty_display_name", identity.ErrEmptyDisplayName, CodeInvalidParams},
		{"identity/empty_role", identity.ErrEmptyRole, CodeInvalidParams},
		{"identity/user_not_found", identity.ErrUserNotFound, CodeNotFound},
		{"identity/agent_not_found", identity.ErrAgentNotFound, CodeNotFound},
		{"identity/not_implemented", identity.ErrNotImplemented, CodeNotImplemented},

		{"repositories/invalid_slug", repositories.ErrInvalidSlug, CodeInvalidParams},
		{"repositories/empty_name", repositories.ErrEmptyName, CodeInvalidParams},
		{"repositories/invalid_visibility", repositories.ErrInvalidVisibility, CodeInvalidParams},
		{"repositories/not_found", repositories.ErrRepositoryNotFound, CodeNotFound},
		{"repositories/conflict", repositories.ErrSlugAlreadyExists, CodeConflict},

		{"restapi/invalid_token", restapi.ErrInvalidToken, CodeUnauthenticated},

		{"deadline", context.DeadlineExceeded, CodeInternal},
		{"unknown", errors.New("boom"), CodeInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := mapErr(context.Background(), c.err)
			if got != c.want {
				t.Errorf("mapErr(%v) code = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

// TestMapErr_WrappedSentinel ensures errors.Is wrapping is honoured.
// Tools that wrap a sentinel with %w must still surface as the
// sentinel's mapped code.
func TestMapErr_WrappedSentinel(t *testing.T) {
	wrapped := fmt.Errorf("at boundary: %w", repositories.ErrInvalidSlug)
	if got, _ := mapErr(context.Background(), wrapped); got != CodeInvalidParams {
		t.Errorf("wrapped invalid_slug got %d, want %d", got, CodeInvalidParams)
	}
}
