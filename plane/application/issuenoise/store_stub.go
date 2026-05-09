package issuenoise

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// StubStore is an in-memory Store for unit tests. Concurrent-safe.
// State is intentionally minimal — only what the router asserts on.
type StubStore struct {
	mu        sync.Mutex
	issues    map[uuid.UUID]IssueState
	decisions []Decision
	outbox    []StubOutboxRow
	// FailOnTx, when non-nil, returns the given error from the Tx
	// callback before any state mutations are visible. Used to test
	// the all-or-nothing Tx contract.
	FailOnTx error
}

// StubOutboxRow is what the stub records for each outbox write.
type StubOutboxRow struct {
	EventType   string
	AggregateID uuid.UUID
	Payload     any
}

// NewStubStore constructs an empty stub.
func NewStubStore() *StubStore {
	return &StubStore{
		issues: make(map[uuid.UUID]IssueState),
	}
}

// Transact runs fn against a fresh stubTx. If fn returns nil and
// FailOnTx is nil, the writes commit; otherwise they are discarded
// — same semantics as a real Tx rollback.
func (s *StubStore) Transact(ctx context.Context, fn func(Tx) error) error {
	tx := &stubTx{
		parent:    s,
		issues:    make(map[uuid.UUID]IssueState),
		decisions: nil,
		outbox:    nil,
	}
	if err := fn(tx); err != nil {
		return err
	}
	if s.FailOnTx != nil {
		return s.FailOnTx
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range tx.issues {
		s.issues[k] = v
	}
	s.decisions = append(s.decisions, tx.decisions...)
	s.outbox = append(s.outbox, tx.outbox...)
	return nil
}

// IssueState returns the committed state for an issue, or "" if absent.
func (s *StubStore) IssueState(id uuid.UUID) IssueState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issues[id]
}

// Decisions returns a copy of the committed decision rows.
func (s *StubStore) Decisions() []Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Decision, len(s.decisions))
	copy(out, s.decisions)
	return out
}

// Outbox returns a copy of the committed outbox rows.
func (s *StubStore) Outbox() []StubOutboxRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StubOutboxRow, len(s.outbox))
	copy(out, s.outbox)
	return out
}

type stubTx struct {
	parent    *StubStore
	issues    map[uuid.UUID]IssueState
	decisions []Decision
	outbox    []StubOutboxRow
}

func (t *stubTx) AnchorIssue(_ context.Context, d IssueDraft, state IssueState) error {
	t.issues[d.ID] = state
	return nil
}

func (t *stubTx) SetIssueState(_ context.Context, id uuid.UUID, state IssueState) error {
	t.issues[id] = state
	return nil
}

func (t *stubTx) InsertDecision(_ context.Context, d Decision) error {
	t.decisions = append(t.decisions, d)
	return nil
}

func (t *stubTx) WriteOutbox(_ context.Context, eventType string, aggregateID uuid.UUID, payload any) error {
	t.outbox = append(t.outbox, StubOutboxRow{
		EventType:   eventType,
		AggregateID: aggregateID,
		Payload:     payload,
	})
	return nil
}
