package mcp

import (
	"context"
	"errors"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
)

// Code is a closed enumeration of MCP JSON-RPC error codes returned by
// this server. Codes mirror JSON-RPC 2.0 reserved space (server errors
// in the -32000…-32099 range) plus the standard -32601 / -32602 /
// -32603 set. Adding a value is a public-API change.
type Code int

const (
	// CodeMethodNotFound is the JSON-RPC standard "method not found".
	// Returned for unknown tool names and unknown MCP methods.
	CodeMethodNotFound Code = -32601
	// CodeInvalidParams is the JSON-RPC standard "invalid params".
	// Mapped from validation_failed.
	CodeInvalidParams Code = -32602
	// CodeInternal is the JSON-RPC standard "internal error". The
	// fallthrough; logged at error level by the server.
	CodeInternal Code = -32603

	// CodeUnauthenticated maps the REST CodeUnauthenticated.
	CodeUnauthenticated Code = -32001
	// CodeForbidden maps the REST CodeForbidden.
	CodeForbidden Code = -32002
	// CodeNotFound maps the REST CodeNotFound.
	CodeNotFound Code = -32003
	// CodeNotImplemented signals a tool that exists in the manifest
	// but whose backing service is not wired in this build (e.g.
	// `pr_create` until repositories.Service.CreatePullRequest ships).
	CodeNotImplemented Code = -32004
	// CodeConflict maps the REST CodeConflict (uniqueness violation).
	CodeConflict Code = -32005
	// CodeRateLimited maps the REST CodeRateLimited.
	CodeRateLimited Code = -32006
)

// ErrNotImplemented is the sentinel a tool returns when its backing
// service is not wired. Mapped to CodeNotImplemented by mapErr. Defined
// here (rather than in each tool) so the MCP layer owns the wire-level
// contract.
var ErrNotImplemented = errors.New("mcp: tool not implemented in this build")

// ErrForbidden is the sentinel a tool returns when the principal
// cannot perform the requested action (e.g. cloning a repo they do
// not have access to).
var ErrForbidden = errors.New("mcp: forbidden")

// mapErr translates an internal error into the wire-level (Code, message)
// pair. Exhaustive over the application-plane sentinels the MCP package
// can encounter; the default arm logs and returns CodeInternal so a
// silent fallthrough cannot leak.
//
// ctx is used only to surface deadline-exceeded vs. unhandled errors;
// the caller is responsible for any structured logging on the result.
func mapErr(_ context.Context, err error) (Code, string) {
	switch {
	case err == nil:
		return CodeInternal, "nil error" // never call mapErr on nil; defensive.

	// MCP-local sentinels.
	case errors.Is(err, ErrNotImplemented):
		return CodeNotImplemented, "tool not implemented"
	case errors.Is(err, ErrForbidden):
		return CodeForbidden, "forbidden"

	// Identity sentinels.
	case errors.Is(err, identity.ErrInvalidEmail),
		errors.Is(err, identity.ErrEmptyDisplayName),
		errors.Is(err, identity.ErrEmptyRole):
		return CodeInvalidParams, err.Error()
	case errors.Is(err, identity.ErrUserNotFound),
		errors.Is(err, identity.ErrAgentNotFound):
		return CodeNotFound, err.Error()
	case errors.Is(err, identity.ErrNotImplemented):
		return CodeNotImplemented, "identity backend not implemented"

	// Repositories sentinels.
	case errors.Is(err, repositories.ErrInvalidSlug),
		errors.Is(err, repositories.ErrEmptyName),
		errors.Is(err, repositories.ErrInvalidVisibility):
		return CodeInvalidParams, err.Error()
	case errors.Is(err, repositories.ErrRepositoryNotFound):
		return CodeNotFound, err.Error()
	case errors.Is(err, repositories.ErrSlugAlreadyExists):
		return CodeConflict, err.Error()

	// REST API sentinels (when bubbled up via the in-process loopback).
	case errors.Is(err, restapi.ErrInvalidToken):
		return CodeUnauthenticated, "invalid bearer token"

	// Plumbing.
	case errors.Is(err, context.DeadlineExceeded):
		return CodeInternal, "deadline exceeded"

	default:
		return CodeInternal, "internal error"
	}
}
