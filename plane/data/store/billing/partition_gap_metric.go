package billing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PartitionUpperBoundProbe returns the upper bound of the latest existing
// monthly partition of billing.usage_events and the total partition count.
type PartitionUpperBoundProbe interface {
	MaxPartitionUpperBound(ctx context.Context) (upperBound time.Time, count int, err error)
}

// PartitionGapMetric exposes Prometheus gauges that surface the calendar
// distance between today and the first INSERT-failing date for
// billing.usage_events.
type PartitionGapMetric struct {
	probe        PartitionUpperBoundProbe
	clock        func() time.Time
	daysUntilGap *prometheus.GaugeVec
	partCount    *prometheus.GaugeVec
}

// NewPartitionGapMetric registers gauges against reg and probes via pool.
func NewPartitionGapMetric(pool *pgxpool.Pool, reg prometheus.Registerer) *PartitionGapMetric {
	return NewPartitionGapMetricWithProbe(&pgProbe{pool: pool}, reg, func() time.Time { return time.Now().UTC() })
}

// NewPartitionGapMetricWithProbe is the test seam.
func NewPartitionGapMetricWithProbe(p PartitionUpperBoundProbe, reg prometheus.Registerer, clock func() time.Time) *PartitionGapMetric {
	auto := promauto.With(reg)
	return &PartitionGapMetric{
		probe: p,
		clock: clock,
		daysUntilGap: auto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gitscale_billing_partition_days_until_gap",
			Help: "Days remaining before billing.usage_events INSERTs will fail",
		}, []string{"schema", "table"}),
		partCount: auto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gitscale_billing_partition_count",
			Help: "Number of monthly partitions currently attached to billing.usage_events",
		}, []string{"schema", "table"}),
	}
}

// Refresh recomputes both gauges. Safe to call on a ticker.
func (m *PartitionGapMetric) Refresh(ctx context.Context) error {
	ub, count, err := m.probe.MaxPartitionUpperBound(ctx)
	if err != nil {
		return fmt.Errorf("partition gap metric: probe: %w", err)
	}
	days := int(ub.Sub(m.clock()).Hours() / 24)
	m.daysUntilGap.WithLabelValues("billing", "usage_events").Set(float64(days))
	m.partCount.WithLabelValues("billing", "usage_events").Set(float64(count))
	return nil
}

type pgProbe struct {
	pool *pgxpool.Pool
}

// MaxPartitionUpperBound parses the upper bound expression of every child
// partition of billing.usage_events and returns the maximum.
//
// EXPLAIN reasoning: pg_inherits + pg_class is a tiny system-catalog scan
// (≪ 100 rows for monthly partitions even after years of growth); no index
// is needed. The window-aggregate count(*) OVER () materialises once.
func (p *pgProbe) MaxPartitionUpperBound(ctx context.Context) (time.Time, int, error) {
	const q = `
SELECT pg_get_expr(c.relpartbound, c.oid) AS bound,
       count(*) OVER ()                  AS count
FROM   pg_inherits i
JOIN   pg_class    c ON c.oid = i.inhrelid
JOIN   pg_class    p ON p.oid = i.inhparent
JOIN   pg_namespace n ON n.oid = p.relnamespace
WHERE  n.nspname = 'billing' AND p.relname = 'usage_events'`
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return time.Time{}, 0, err
	}
	defer rows.Close()

	var (
		max   time.Time
		count int
	)
	for rows.Next() {
		var bound string
		if err := rows.Scan(&bound, &count); err != nil {
			return time.Time{}, 0, err
		}
		ub, err := parsePartitionUpperBound(bound)
		if err != nil {
			return time.Time{}, 0, err
		}
		if ub.After(max) {
			max = ub
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, 0, err
	}
	return max, count, nil
}

// parsePartitionUpperBound extracts the TO ('...') date from a
// `pg_get_expr` partition-bound string. Postgres renders the bound in
// either of two forms depending on the partition column type:
//
//	FOR VALUES FROM ('2026-05-01') TO ('2026-06-01')                   -- date
//	FOR VALUES FROM ('2026-05-01 00:00:00+00') TO ('2026-06-01 00:00:00+00') -- timestamptz
//
// We accept either by parsing the leading YYYY-MM-DD prefix.
func parsePartitionUpperBound(expr string) (time.Time, error) {
	const marker = "TO ('"
	i := strings.Index(expr, marker)
	if i < 0 {
		return time.Time{}, fmt.Errorf("partition gap metric: cannot find TO clause in %q", expr)
	}
	rest := expr[i+len(marker):]
	end := strings.Index(rest, "'")
	if end < 0 {
		return time.Time{}, fmt.Errorf("partition gap metric: unterminated TO clause in %q", expr)
	}
	literal := rest[:end]
	// Strip any time-of-day / tz suffix; we only need the date.
	if sp := strings.Index(literal, " "); sp >= 0 {
		literal = literal[:sp]
	}
	return time.Parse("2006-01-02", literal)
}
