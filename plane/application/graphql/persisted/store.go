// Package persisted is the GraphQL persisted-query store.
//
// Persisted queries are immutable: a `(hash, query)` pair is registered
// once and the hash thereafter resolves to the same query body. The store
// is read-heavy (every persisted call hits Get); hot reads are served by a
// CacheStore wrapper (cached_store.go) over a Postgres-backed source of
// truth (postgres_store.go).
package persisted

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
)

// Sentinel errors. Callers map ErrNotFound to extensions.code =
// "PERSISTED_QUERY_NOT_FOUND" and ErrHashConflict to "VALIDATION_FAILED".
var (
	ErrNotFound     = errors.New("persisted: query not found")
	ErrHashConflict = errors.New("persisted: hash collision with different body")
)

// HashPrefix is the canonical prefix used in the wire protocol. Clients
// register a query and receive `sha256:<hex>`; subsequent execute calls
// use the same string verbatim.
const HashPrefix = "sha256:"

// HashFor returns the canonical persisted-query hash for the given body.
// The body is hashed verbatim — no whitespace normalisation — so callers
// must agree on the exact byte sequence.
func HashFor(query string) string {
	sum := sha256.Sum256([]byte(query))
	return HashPrefix + hex.EncodeToString(sum[:])
}

// Store is the persisted-query interface. Implementations must be safe for
// concurrent use.
type Store interface {
	// Get returns the query body for hash, or ErrNotFound on a miss.
	Get(ctx context.Context, hash string) (string, error)
	// Put inserts (hash, query, registeredBy). It is idempotent on
	// (hash, query) — re-registering the same pair returns nil. A hash
	// collision with a different body returns ErrHashConflict.
	Put(ctx context.Context, hash, query string, registeredBy uuid.UUID) error
}
