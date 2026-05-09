package sink

import (
	"context"
	"sync"

	"github.com/gitscale-platform/gitscale/plane/git/metering"
)

// StubSink is an in-memory AnalyticsSink for tests and local development.
// Events are appended in arrival order; All() returns a snapshot copy.
//
// Idempotency is enforced on EventID — replaying the same event is a
// no-op so tests can verify at-least-once delivery without false positives
// in length assertions.
type StubSink struct {
	mu     sync.Mutex
	events []metering.MeteringEvent
	seen   map[string]struct{}
}

// NewStubSink returns an empty StubSink.
func NewStubSink() *StubSink {
	return &StubSink{seen: make(map[string]struct{})}
}

// Record appends e if its EventID has not been seen before. Returns nil in
// every case; the in-memory backend cannot fail. ctx is accepted for
// interface compatibility but not used.
func (s *StubSink) Record(_ context.Context, e metering.MeteringEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.EventID != "" {
		if _, dup := s.seen[e.EventID]; dup {
			return nil
		}
		s.seen[e.EventID] = struct{}{}
	}
	s.events = append(s.events, e)
	return nil
}

// All returns a snapshot of recorded events in arrival order.
// Test helper only — production callers should drive the sink and never
// inspect its internal state.
func (s *StubSink) All() []metering.MeteringEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]metering.MeteringEvent, len(s.events))
	copy(out, s.events)
	return out
}

// Len returns the count of distinct events recorded.
func (s *StubSink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// Compile-time interface check.
var _ AnalyticsSink = (*StubSink)(nil)
