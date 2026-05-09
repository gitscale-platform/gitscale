package persisted

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PgxExecutor is the minimal subset of *pgxpool.Pool / pgx.Tx behaviour
// the Postgres-backed persisted store needs. Tests pass a fake; production
// passes a pgxpool.Pool.
//
// Keeping the dependency surface this small avoids dragging the entire pg
// driver into the resolver layer — only this file imports pgx, mirroring
// the plane/data/store/postgres pattern (ADR-017 swap surface preserved by
// having the application plane reach the store through a Store interface).
type PgxExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PostgresStore is the source-of-truth Store backed by Postgres.
type PostgresStore struct {
	conn PgxExecutor
}

// NewPostgresStore returns a store backed by conn.
func NewPostgresStore(conn PgxExecutor) *PostgresStore {
	return &PostgresStore{conn: conn}
}

// Get implements Store.
func (s *PostgresStore) Get(ctx context.Context, hash string) (string, error) {
	var query string
	row := s.conn.QueryRow(ctx,
		`SELECT query FROM graphql.persisted_queries WHERE hash = $1`,
		hash)
	if err := row.Scan(&query); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return query, nil
}

// Put implements Store with insert-or-verify semantics:
//   - INSERT ... ON CONFLICT (hash) DO NOTHING.
//   - If the conflict path was taken (RowsAffected = 0), re-read and
//     compare bodies. Match → nil; mismatch → ErrHashConflict.
//
// The two queries do not need to share a transaction: a racing caller
// with the same hash and a *different* body is a hash collision regardless
// of which insert wins; both outcomes are correct (winner stored, loser
// observes ErrHashConflict).
func (s *PostgresStore) Put(ctx context.Context, hash, query string, registeredBy uuid.UUID) error {
	tag, err := s.conn.Exec(ctx,
		`INSERT INTO graphql.persisted_queries (hash, query, registered_by)
		   VALUES ($1, $2, $3)
		   ON CONFLICT (hash) DO NOTHING`,
		hash, query, registeredBy)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	existing, err := s.Get(ctx, hash)
	if err != nil {
		return err
	}
	if existing != query {
		return ErrHashConflict
	}
	return nil
}
