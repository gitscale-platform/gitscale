package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsRetryable reports whether err is a PostgreSQL serialization failure
// (SQLSTATE 40001). Callers that use serializable transactions should retry
// on true and surface the error otherwise.
func IsRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001"
	}
	return false
}
