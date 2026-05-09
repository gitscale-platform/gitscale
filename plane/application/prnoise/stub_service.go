package prnoise

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// OutboxRecord captures one outbox emission for upstream test
// assertions. Mirrors the shape used by plane/data/store/stub.
type OutboxRecord struct {
	EventID   uuid.UUID
	EventType string
	PRID      uuid.UUID
	Payload   NoiseDecisionRecordedPayload
}

// StubService is an in-memory Service for upstream package tests
// (PR engine handler, webhook delivery worker). Decisions and outbox
// rows are recorded together to mirror the postgres impl's atomic
// commit semantics.
//
// Re-scoring an existing PR upserts the decision row AND appends a new
// outbox record — the postgres impl behaves identically (downstream
// consumers idempotency-key on event_id).
type StubService struct {
	mu        sync.Mutex
	pipeline  *Pipeline
	clock     func() time.Time
	decisions map[uuid.UUID]Decision
	outbox    []OutboxRecord
}

// NewStubService returns an empty StubService driven by p. Pipeline
// must be non-nil; ErrPipelineUnconfigured is returned otherwise.
func NewStubService(p *Pipeline) (*StubService, error) {
	if p == nil {
		return nil, ErrPipelineUnconfigured
	}
	return &StubService{
		pipeline:  p,
		clock:     func() time.Time { return time.Now().UTC() },
		decisions: make(map[uuid.UUID]Decision),
	}, nil
}

// SetClock overrides the clock used to fill DecidedAt for inputs that
// did not provide it. Test helper.
func (s *StubService) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = now
}

// RecordDecision implements Service. Pipeline.Score errors are returned
// without persisting anything.
func (s *StubService) RecordDecision(ctx context.Context, in PRInput) (Decision, error) {
	d, err := s.pipeline.Score(ctx, in)
	if err != nil {
		return Decision{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if d.DecidedAt.IsZero() {
		d.DecidedAt = s.clock()
	}
	s.decisions[d.PRID] = d
	s.outbox = append(s.outbox, OutboxRecord{
		EventID:   uuid.New(),
		EventType: EventTypeNoiseDecisionRecorded,
		PRID:      d.PRID,
		Payload:   newNoiseDecisionRecordedPayload(d),
	})
	return d, nil
}

// Decisions returns a snapshot of recorded decisions. Test helper.
func (s *StubService) Decisions() map[uuid.UUID]Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[uuid.UUID]Decision, len(s.decisions))
	for k, v := range s.decisions {
		out[k] = v
	}
	return out
}

// Outbox returns a snapshot of recorded outbox rows in insertion order.
func (s *StubService) Outbox() []OutboxRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]OutboxRecord, len(s.outbox))
	copy(out, s.outbox)
	return out
}
