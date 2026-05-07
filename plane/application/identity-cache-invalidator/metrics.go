package invalidator

import (
	"sync"
	"sync/atomic"
)

// Result labels for invalidations_total. Closed set; new entries surface as
// "unknown_result" until the registry is updated, which is the desired loud
// failure mode.
const (
	ResultOK               = "ok"
	ResultAlreadyProcessed = "already_processed"
	ResultUnknownEventType = "unknown_event_type"
	ResultHandlerError     = "handler_error"
	ResultCacheError       = "cache_error"
	ResultEnvelopeDecode   = "envelope_decode_failed"
)

// Metrics is the Prometheus-shape registry used by the consumer. It is
// intentionally minimal — a counter map keyed by (event_type, result) plus
// an oldest-unprocessed gauge — to avoid pulling in the full Prometheus
// client until the platform-wide registry rollout (separate epic). The
// /metrics endpoint scraper queries this struct directly via Snapshot().
type Metrics struct {
	mu             sync.Mutex
	invalidations  map[counterKey]uint64
	dlq            map[counterKey]uint64
	consumerLagSec atomic.Int64
	oldestEventSec atomic.Int64
}

type counterKey struct {
	EventType string
	Label     string // result for invalidations, reason for dlq
}

func NewMetrics() *Metrics {
	return &Metrics{
		invalidations: make(map[counterKey]uint64),
		dlq:           make(map[counterKey]uint64),
	}
}

func (m *Metrics) IncInvalidation(eventType, result string) {
	m.mu.Lock()
	m.invalidations[counterKey{eventType, result}]++
	m.mu.Unlock()
}

func (m *Metrics) IncDLQ(eventType, reason string) {
	m.mu.Lock()
	m.dlq[counterKey{eventType, reason}]++
	m.mu.Unlock()
}

func (m *Metrics) SetConsumerLagSeconds(v int64)  { m.consumerLagSec.Store(v) }
func (m *Metrics) SetOldestEventSeconds(v int64)  { m.oldestEventSec.Store(v) }

// Snapshot returns a copy of the current counter map. Used by the metrics
// HTTP handler and by tests.
func (m *Metrics) Snapshot() (invalidations, dlq map[counterKey]uint64, consumerLag, oldest int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv := make(map[counterKey]uint64, len(m.invalidations))
	for k, v := range m.invalidations {
		inv[k] = v
	}
	d := make(map[counterKey]uint64, len(m.dlq))
	for k, v := range m.dlq {
		d[k] = v
	}
	return inv, d, m.consumerLagSec.Load(), m.oldestEventSec.Load()
}

// InvalidationCount returns the count for one (event_type, result) cell.
// Test convenience.
func (m *Metrics) InvalidationCount(eventType, result string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.invalidations[counterKey{eventType, result}]
}
