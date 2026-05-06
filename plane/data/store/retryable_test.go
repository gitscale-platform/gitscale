package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsRetryable(t *testing.T) {
	t.Run("returns true for 40001", func(t *testing.T) {
		err := &pgconn.PgError{Code: "40001"}
		if !IsRetryable(err) {
			t.Fatal("expected IsRetryable to return true for SQLSTATE 40001")
		}
	})

	t.Run("returns true for wrapped 40001", func(t *testing.T) {
		err := fmt.Errorf("tx failed: %w", &pgconn.PgError{Code: "40001"})
		if !IsRetryable(err) {
			t.Fatal("expected IsRetryable to return true for wrapped 40001")
		}
	})

	t.Run("returns false for other SQL errors", func(t *testing.T) {
		err := &pgconn.PgError{Code: "23505"} // unique_violation
		if IsRetryable(err) {
			t.Fatal("expected IsRetryable to return false for 23505")
		}
	})

	t.Run("returns false for non-pgx errors", func(t *testing.T) {
		if IsRetryable(errors.New("some other error")) {
			t.Fatal("expected IsRetryable to return false for non-pgx error")
		}
	})

	t.Run("returns false for nil", func(t *testing.T) {
		if IsRetryable(nil) {
			t.Fatal("expected IsRetryable to return false for nil")
		}
	})
}
