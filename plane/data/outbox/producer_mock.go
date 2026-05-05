package outbox

import (
	"context"
	"encoding/json"
	"sync"

	kafkadata "github.com/gitscale-platform/gitscale/plane/data/kafka"
	"github.com/google/uuid"
)

// MockProducer is an in-memory KafkaProducer implementation for unit tests.
//
// It is safe for concurrent use and records every published EventEnvelope so
// tests can inspect them. An optional InjectErr field causes PublishBatch to
// return that error, simulating a broker failure.
type MockProducer struct {
	mu       sync.Mutex
	messages map[string][]kafkadata.EventEnvelope // topic -> ordered envelopes
	closed   bool

	// InjectErr, if non-nil, is returned by PublishBatch. Useful for testing
	// the rollback path (spec §16: publish error → no UPDATE issued).
	InjectErr error

	// PublishAfterN, if > 0, simulates a partial crash: publishes the first
	// PublishAfterN messages then returns InjectErr. This exercises the
	// crash-mid-batch dedupe test case (spec §16).
	PublishAfterN int
}

// NewMockProducer returns an initialised MockProducer.
func NewMockProducer() *MockProducer {
	return &MockProducer{
		messages: make(map[string][]kafkadata.EventEnvelope),
	}
}

// PublishBatch records each event in the in-memory store unless InjectErr is set.
func (m *MockProducer) PublishBatch(ctx context.Context, topic string, batch []OutboxRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.PublishAfterN > 0 {
		limit := m.PublishAfterN
		if limit > len(batch) {
			limit = len(batch)
		}
		for i := 0; i < limit; i++ {
			m.appendLocked(topic, batch[i])
		}
		return m.InjectErr
	}

	if m.InjectErr != nil {
		return m.InjectErr
	}

	for _, row := range batch {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.appendLocked(topic, row)
	}
	return nil
}

// Close marks the producer as closed. Idempotent.
func (m *MockProducer) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// Messages returns a copy of all envelopes published to topic, in order.
func (m *MockProducer) Messages(topic string) []kafkadata.EventEnvelope {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]kafkadata.EventEnvelope, len(m.messages[topic]))
	copy(out, m.messages[topic])
	return out
}

// EventIDs returns the set of event_ids published to topic.
func (m *MockProducer) EventIDs(topic string) map[uuid.UUID]struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[uuid.UUID]struct{}, len(m.messages[topic]))
	for _, env := range m.messages[topic] {
		out[env.EventID] = struct{}{}
	}
	return out
}

// IsClosed reports whether Close has been called.
func (m *MockProducer) IsClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *MockProducer) appendLocked(topic string, row OutboxRow) {
	env := kafkadata.EventEnvelope{
		EventID:       row.EventID,
		AggregateType: row.AggregateType,
		AggregateID:   row.AggregateID,
		EventType:     row.EventType,
		Payload:       json.RawMessage(row.Payload),
		CreatedAt:     row.CreatedAt,
	}
	m.messages[topic] = append(m.messages[topic], env)
}
