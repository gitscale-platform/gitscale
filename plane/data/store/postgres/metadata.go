// Package postgres implements MetadataStore against a PostgreSQL database via
// pgx. Transactions are opened at ISOLATION LEVEL SERIALIZABLE; serialization
// failures (SQLSTATE 40001) are surfaced to the caller for retry.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errNotImplemented = errors.New("not implemented")

// querier is the minimal pgx query interface satisfied by both pgxpool.Pool
// and pgx.Tx, allowing IdentityReader to work inside and outside transactions.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Store implements store.MetadataStore over a pgxpool.Pool.
type Store struct {
	db *pgxpool.Pool
}

// New returns a Store backed by db.
func New(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// Transact opens a serializable transaction and runs fn. If fn returns nil
// the transaction is committed; otherwise it is rolled back and the error is
// returned. Serialization failures (40001) are returned verbatim so callers
// can detect them via store.IsRetryable.
func (s *Store) Transact(ctx context.Context, fn func(store.Tx) error) error {
	txOpts := pgx.TxOptions{IsoLevel: pgx.Serializable}
	tx, err := s.db.BeginTx(ctx, txOpts)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer func() {
		// Best-effort rollback; ignored because fn's error is already returned.
		_ = tx.Rollback(ctx)
	}()

	wrapper := &txWrapper{tx: tx}
	if err := fn(wrapper); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// Identity returns the identity reader bound to the pool (for reads outside
// a transaction).
func (s *Store) Identity() store.IdentityReader {
	return &identityReader{q: s.db}
}

// Repositories returns the repository reader bound to the pool.
func (s *Store) Repositories() store.RepositoryReader {
	return &repositoryReader{q: s.db}
}

// Billing returns the billing reader bound to the pool.
func (s *Store) Billing() store.BillingReader {
	return &billingReader{q: s.db}
}

// txWrapper adapts a pgx.Tx to the store.Tx interface.
type txWrapper struct {
	tx pgx.Tx
}

func (w *txWrapper) Identity() store.IdentityWriter {
	return &identityWriter{identityReader: identityReader{q: w.tx}}
}

func (w *txWrapper) Repositories() store.RepositoryWriter {
	return &repositoryWriter{repositoryReader: repositoryReader{q: w.tx}}
}

func (w *txWrapper) Billing() store.BillingWriter {
	return &billingWriter{billingReader: billingReader{q: w.tx}}
}

// WriteOutbox inserts a row into the domain-specific outbox table within the
// active transaction. domain must be a valid store.Domain constant.
func (w *txWrapper) WriteOutbox(
	ctx context.Context,
	domain store.Domain,
	aggregateType string,
	aggregateID uuid.UUID,
	eventType string,
	payload any,
) error {
	if !domain.Valid() {
		return fmt.Errorf("postgres: WriteOutbox: invalid domain %q", domain)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("postgres: WriteOutbox: marshal payload: %w", err)
	}
	table := domain.OutboxTable()
	q := fmt.Sprintf(
		`INSERT INTO %s (event_id, aggregate_type, aggregate_id, event_type, payload)
		 VALUES ($1, $2, $3, $4, $5)`,
		table,
	)
	eventID := store.NewEventID()
	_, err = w.tx.Exec(ctx, q, eventID, aggregateType, aggregateID, eventType, raw)
	if err != nil {
		return fmt.Errorf("postgres: WriteOutbox %s: %w", table, err)
	}
	return nil
}
