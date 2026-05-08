package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Expirer deletes processed outbox rows older than TTL in bounded batches,
// per ADR-008 ("outbox rows TTL-expire 24h after the consumer high-water
// mark advances past them"). One Expirer per domain.
//
// Only rows with processed_at IS NOT NULL AND processed_at < now() - TTL are
// deleted. Unprocessed rows are never touched, so the consumer's
// SELECT ... FOR UPDATE SKIP LOCKED stream is unaffected.
type Expirer struct {
	pool      *pgxpool.Pool
	domain    store.Domain
	ttl       time.Duration
	batchSize int
	metrics   *ExpirerMetrics
}

// ExpirerOptions configures an Expirer. Zero values resolve to defaults.
type ExpirerOptions struct {
	// TTL is the minimum age of processed rows eligible for deletion.
	// Zero defaults to 24 hours (the value mandated by ADR-008).
	TTL time.Duration
	// BatchSize caps the rows deleted per cycle so a backlog does not become
	// a single multi-million-row DELETE. Zero defaults to 10000.
	BatchSize int
	// Registry receives Prometheus instruments. nil means metrics are wired
	// to a discard registry (callable but unobservable).
	Registry prometheus.Registerer
}

// NewExpirer constructs an Expirer for d. Panics if d is not a valid domain
// — invalid domain at boot is a programmer error, not a runtime condition.
func NewExpirer(pool *pgxpool.Pool, d store.Domain, opts ExpirerOptions) *Expirer {
	if !d.Valid() {
		panic(fmt.Sprintf("outbox.NewExpirer: invalid domain %q", d))
	}
	if opts.TTL <= 0 {
		opts.TTL = 24 * time.Hour
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 10000
	}
	return &Expirer{
		pool:      pool,
		domain:    d,
		ttl:       opts.TTL,
		batchSize: opts.BatchSize,
		metrics:   NewExpirerMetrics(opts.Registry),
	}
}

// Domain returns the domain this Expirer targets.
func (e *Expirer) Domain() store.Domain { return e.domain }

// Expire deletes expired rows in batches until a cycle deletes fewer than
// batchSize rows. Returns the total deleted across all cycles.
func (e *Expirer) Expire(ctx context.Context) (int64, error) {
	start := time.Now()
	var total int64
	for {
		n, err := e.expireBatch(ctx)
		total += n
		if err != nil {
			e.metrics.observe(string(e.domain), total, time.Since(start))
			return total, err
		}
		if n < int64(e.batchSize) {
			break
		}
	}
	e.metrics.observe(string(e.domain), total, time.Since(start))
	return total, nil
}

// expireBatch performs one bounded DELETE. The CTE keeps deletion ordered
// by id (oldest first) and bounded by LIMIT.
func (e *Expirer) expireBatch(ctx context.Context) (int64, error) {
	table := e.domain.OutboxTable()
	//nolint:gosec // table name is derived from a typed Domain enum, not user input
	q := fmt.Sprintf(`
WITH victims AS (
    SELECT id FROM %s
    WHERE processed_at IS NOT NULL
      AND processed_at < now() - $1::interval
    ORDER BY id
    LIMIT $2
)
DELETE FROM %s
WHERE id IN (SELECT id FROM victims)`, table, table)
	tag, err := e.pool.Exec(ctx, q, e.ttl, e.batchSize)
	if err != nil {
		return 0, fmt.Errorf("outbox expirer: %s: %w", e.domain, err)
	}
	return tag.RowsAffected(), nil
}
