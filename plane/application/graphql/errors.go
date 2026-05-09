package graphql

import (
	"context"
	"errors"
	"log/slog"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/persisted"
	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
)

// ErrorCode is the closed enum of GraphQL extensions.code values. Adding a
// value is a public-API change.
type ErrorCode string

const (
	CodeUnauthenticated       ErrorCode = "UNAUTHENTICATED"
	CodeForbidden             ErrorCode = "FORBIDDEN"
	CodeNotFound              ErrorCode = "NOT_FOUND"
	CodeValidationFailed      ErrorCode = "VALIDATION_FAILED"
	CodeRateLimited           ErrorCode = "RATE_LIMITED"
	CodeFieldNotSupported     ErrorCode = "FIELD_NOT_SUPPORTED"
	CodeCostBudgetExceeded    ErrorCode = "COST_BUDGET_EXCEEDED"
	CodeDepthExceeded         ErrorCode = "DEPTH_EXCEEDED"
	CodePersistedQueryNotFound ErrorCode = "PERSISTED_QUERY_NOT_FOUND"
	CodePersistedQueryConflict ErrorCode = "PERSISTED_QUERY_CONFLICT"
	CodeNotImplemented        ErrorCode = "NOT_IMPLEMENTED"
	CodeInternal              ErrorCode = "INTERNAL"
)

// Error is the GraphQL error envelope. JSON-marshalled directly into the
// `errors[]` array of the response.
type Error struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// NewError constructs an Error with the supplied code and message.
func NewError(code ErrorCode, msg string) Error {
	return Error{
		Message:    msg,
		Extensions: map[string]any{"code": string(code)},
	}
}

// withRequestID returns e with extensions.request_id populated.
func (e Error) withRequestID(rid string) Error {
	if rid == "" {
		return e
	}
	if e.Extensions == nil {
		e.Extensions = map[string]any{}
	}
	e.Extensions["request_id"] = rid
	return e
}

// MapErr is the exhaustive sentinel→ErrorCode translator. The default arm
// logs at error level and returns CodeInternal so the caller never sees a
// silent fallthrough.
func MapErr(ctx context.Context, logger *slog.Logger, err error) (ErrorCode, string) {
	switch {
	case err == nil:
		return CodeInternal, "nil error"

	// Cost analysis.
	case errors.Is(err, cost.ErrDepthExceeded):
		return CodeDepthExceeded, err.Error()
	case errors.Is(err, cost.ErrCostBudgetExceeded):
		return CodeCostBudgetExceeded, err.Error()
	case errors.Is(err, cost.ErrParse),
		errors.Is(err, cost.ErrUnknownOperation),
		errors.Is(err, cost.ErrAmbiguousOperation),
		errors.Is(err, cost.ErrFragmentCycle),
		errors.Is(err, cost.ErrFragmentNotDefined):
		return CodeValidationFailed, err.Error()

	// Persisted queries.
	case errors.Is(err, persisted.ErrNotFound):
		return CodePersistedQueryNotFound, err.Error()
	case errors.Is(err, persisted.ErrHashConflict):
		return CodePersistedQueryConflict, err.Error()

	// Identity / repositories.
	case errors.Is(err, identity.ErrAgentNotFound),
		errors.Is(err, identity.ErrUserNotFound),
		errors.Is(err, repositories.ErrRepositoryNotFound):
		return CodeNotFound, err.Error()
	case errors.Is(err, identity.ErrInvalidEmail),
		errors.Is(err, identity.ErrEmptyDisplayName),
		errors.Is(err, identity.ErrEmptyRole),
		errors.Is(err, repositories.ErrInvalidSlug),
		errors.Is(err, repositories.ErrEmptyName),
		errors.Is(err, repositories.ErrInvalidVisibility):
		return CodeValidationFailed, err.Error()
	case errors.Is(err, identity.ErrNotImplemented):
		return CodeNotImplemented, "not implemented"

	case errors.Is(err, context.DeadlineExceeded):
		return CodeInternal, "deadline exceeded"

	default:
		if logger != nil {
			logger.ErrorContext(ctx, "graphql: unhandled error", slog.String("err", err.Error()))
		}
		return CodeInternal, "internal error"
	}
}
