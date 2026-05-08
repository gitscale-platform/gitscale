package outbox

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus handles for one consumer instance.
// Registered against the provided registerer; uses promauto for safety.
//
// All methods are nil-safe: if m == nil, every call is a no-op. This lets
// tests construct consumers without a registry.
type Metrics struct {
	// drainCyclesTotal counts drain loop iterations, labelled by result.
	// result values: "ok", "lock_missed", "empty", "publish_error", "update_error"
	drainCyclesTotal *prometheus.CounterVec

	// batchSize records the number of rows in each successful drain batch.
	batchSize *prometheus.HistogramVec

	// publishDuration records the duration of each PublishBatch call.
	publishDuration *prometheus.HistogramVec

	// highWaterLag is the age in seconds of the oldest unprocessed outbox
	// row, sampled every poll cycle regardless of whether this replica holds
	// the advisory lock. Tracks ADR-008's high-water mark — the time horizon
	// up to which every outbox row has been published to Kafka.
	// Alert at > 60s sustained (spec §12).
	highWaterLag *prometheus.GaugeVec

	// processedTotal is the cumulative count of successfully processed rows.
	processedTotal *prometheus.CounterVec

	// advisoryLockHeld is 1 when this replica holds the advisory lock for the
	// current cycle, 0 otherwise.
	advisoryLockHeld *prometheus.GaugeVec
}

// NewMetrics creates and registers the Prometheus metric handles for a single
// domain's consumer. reg may be prometheus.DefaultRegisterer.
func NewMetrics(domain string, reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	labels := prometheus.Labels{"domain": domain}

	m := &Metrics{}

	m.drainCyclesTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name:        "outbox_drain_cycles_total",
		Help:        "Number of outbox drain loop iterations, labelled by result.",
		ConstLabels: labels,
	}, []string{"result"})

	m.batchSize = factory.NewHistogramVec(prometheus.HistogramOpts{
		Name:        "outbox_batch_size",
		Help:        "Number of rows in each successfully drained batch.",
		ConstLabels: labels,
		Buckets:     []float64{1, 5, 10, 25, 50, 100, 250},
	}, []string{})

	m.publishDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Name:        "outbox_publish_duration_seconds",
		Help:        "Duration of each PublishBatch call in seconds.",
		ConstLabels: labels,
		Buckets:     prometheus.DefBuckets,
	}, []string{"result"})

	m.highWaterLag = factory.NewGaugeVec(prometheus.GaugeOpts{
		Name:        "outbox_consumer_high_water_lag_seconds",
		Help:        "Age in seconds of the oldest unprocessed outbox row. SLO: alert >60s.",
		ConstLabels: labels,
	}, []string{})

	m.processedTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name:        "outbox_processed_total",
		Help:        "Cumulative count of outbox rows successfully published and marked processed.",
		ConstLabels: labels,
	}, []string{})

	m.advisoryLockHeld = factory.NewGaugeVec(prometheus.GaugeOpts{
		Name:        "outbox_advisory_lock_held",
		Help:        "1 when this replica holds the advisory lock for the current drain cycle, 0 otherwise.",
		ConstLabels: labels,
	}, []string{})

	return m
}

func (m *Metrics) incDrainCycles(result string) {
	if m == nil {
		return
	}
	m.drainCyclesTotal.WithLabelValues(result).Inc()
}

func (m *Metrics) observeBatchSize(n int) {
	if m == nil {
		return
	}
	m.batchSize.WithLabelValues().Observe(float64(n))
}

func (m *Metrics) observePublishDuration(seconds float64, result string) {
	if m == nil {
		return
	}
	m.publishDuration.WithLabelValues(result).Observe(seconds)
}

func (m *Metrics) setHighWaterLag(seconds float64) {
	if m == nil {
		return
	}
	m.highWaterLag.WithLabelValues().Set(seconds)
}

func (m *Metrics) addProcessed(n int) {
	if m == nil {
		return
	}
	m.processedTotal.WithLabelValues().Add(float64(n))
}

func (m *Metrics) setAdvisoryLockHeld(held bool) {
	if m == nil {
		return
	}
	v := 0.0
	if held {
		v = 1.0
	}
	m.advisoryLockHeld.WithLabelValues().Set(v)
}
