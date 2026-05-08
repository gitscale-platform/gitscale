package outbox

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ExpirerMetrics holds Prometheus instruments for Expirer observability.
//
// Two instruments per ADR-008 dashboard requirements:
//   - gitscale_outbox_rows_deleted_total{domain}: cumulative rows deleted
//   - gitscale_outbox_expirer_duration_seconds{domain}: wall time per cycle
type ExpirerMetrics struct {
	rowsDeleted *prometheus.CounterVec
	duration    *prometheus.HistogramVec
}

// NewExpirerMetrics constructs metrics registered against reg. nil reg means
// metrics are registered into a fresh discard registry — handy for tests.
func NewExpirerMetrics(reg prometheus.Registerer) *ExpirerMetrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	auto := promauto.With(reg)
	return &ExpirerMetrics{
		rowsDeleted: auto.NewCounterVec(prometheus.CounterOpts{
			Name: "gitscale_outbox_rows_deleted_total",
			Help: "Outbox rows deleted by the TTL expirer, by domain.",
		}, []string{"domain"}),
		duration: auto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gitscale_outbox_expirer_duration_seconds",
			Help:    "Wall time of one Expire cycle, by domain.",
			Buckets: prometheus.DefBuckets,
		}, []string{"domain"}),
	}
}

func (m *ExpirerMetrics) observe(domain string, deleted int64, dur time.Duration) {
	if m == nil {
		return
	}
	m.rowsDeleted.WithLabelValues(domain).Add(float64(deleted))
	m.duration.WithLabelValues(domain).Observe(dur.Seconds())
}
