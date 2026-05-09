package restapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrorCode is a closed enum of stable client-facing error codes. Adding a
// value is a public-API change and requires bumping client SDKs.
type ErrorCode string

const (
	CodeUnauthenticated  ErrorCode = "unauthenticated"
	CodeForbidden        ErrorCode = "forbidden"
	CodeNotFound         ErrorCode = "not_found"
	CodeValidationFailed ErrorCode = "validation_failed"
	CodeConflict         ErrorCode = "conflict"
	CodeRateLimited      ErrorCode = "rate_limited"
	CodeInternal         ErrorCode = "internal"
)

// errorEnvelope is the response shape for any 4xx/5xx response.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	RequestID string    `json:"request_id,omitempty"`
}

// writeError serialises envelope, sets Content-Type, and writes status.
// It is safe to call exactly once per request; subsequent calls are no-ops
// from net/http's perspective but logged here.
func writeError(w http.ResponseWriter, r *http.Request, status int, code ErrorCode, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	body := errorEnvelope{Error: errorBody{Code: code, Message: msg, RequestID: RequestIDFromContext(r.Context())}}
	_ = json.NewEncoder(w).Encode(body)
}

// mapErr is the exhaustive sentinel→(status, code, message) translator.
//
// The default arm logs at error level and returns 500 internal so the
// client never sees a silent fallthrough.
func mapErr(ctx context.Context, logger *slog.Logger, err error) (int, ErrorCode, string) {
	switch {
	case errors.Is(err, identity.ErrInvalidEmail),
		errors.Is(err, identity.ErrEmptyDisplayName),
		errors.Is(err, identity.ErrEmptyRole),
		errors.Is(err, repositories.ErrInvalidSlug),
		errors.Is(err, repositories.ErrEmptyName),
		errors.Is(err, repositories.ErrInvalidVisibility):
		return http.StatusBadRequest, CodeValidationFailed, err.Error()

	case errors.Is(err, identity.ErrUserNotFound),
		errors.Is(err, identity.ErrAgentNotFound),
		errors.Is(err, repositories.ErrRepositoryNotFound):
		return http.StatusNotFound, CodeNotFound, err.Error()

	case errors.Is(err, repositories.ErrSlugAlreadyExists):
		return http.StatusConflict, CodeConflict, err.Error()

	case errors.Is(err, identity.ErrNotImplemented):
		// Treat ErrNotImplemented as 501 with internal-class code so the
		// client sees a stable code and the operator sees the log line.
		if logger != nil {
			logger.WarnContext(ctx, "rest_api: not implemented", slog.String("err", err.Error()))
		}
		return http.StatusNotImplemented, CodeInternal, "not implemented"

	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, CodeInternal, "deadline exceeded"

	case isUniqueViolation(err):
		return http.StatusConflict, CodeConflict, "resource already exists"

	default:
		if logger != nil {
			logger.ErrorContext(ctx, "rest_api: unhandled error", slog.String("err", err.Error()))
		}
		return http.StatusInternalServerError, CodeInternal, "internal error"
	}
}

// isUniqueViolation matches PostgreSQL SQLSTATE 23505 (unique_violation).
// Pulled out as a free function so handlers can also test for it.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
