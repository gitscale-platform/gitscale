package appclient

import (
	"context"
	"sync"
)

// BillingClient is the workflow-plane view of the application-plane billing
// service. RecordPartitionArchived corresponds to a gRPC unary call that writes
// billing.partition_archived to the billing_outbox in a single transaction
// (ADR-008 + ADR-019). The gRPC implementation is a follow-up issue.
type BillingClient interface {
	// RecordPartitionArchived emits billing.partition_archived to the outbox
	// via the billing app-plane service.
	RecordPartitionArchived(ctx context.Context, in PartitionArchivedInput) error
}

// PartitionArchivedInput carries the archival outcome to the billing service.
type PartitionArchivedInput struct {
	Year          int
	Month         int
	PartitionName string
	LakeURI       string // canonical URI returned by ObjectStore.Upload
	RowCount      int64
	BytesWritten  int64
}

// StubBillingClient records calls in memory. Used by workflow unit tests.
type StubBillingClient struct {
	mu    sync.Mutex
	calls []PartitionArchivedInput
	fn    func(in PartitionArchivedInput) error
}

// NewStubBillingClient returns a recording stub that succeeds by default.
func NewStubBillingClient() *StubBillingClient { return &StubBillingClient{} }

// SetFn injects a fake-error path.
func (s *StubBillingClient) SetFn(fn func(PartitionArchivedInput) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn = fn
}

// RecordPartitionArchived records the call and runs the injected fn if set.
func (s *StubBillingClient) RecordPartitionArchived(_ context.Context, in PartitionArchivedInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fn != nil {
		if err := s.fn(in); err != nil {
			return err
		}
	}
	s.calls = append(s.calls, in)
	return nil
}

// Calls returns a snapshot of all recorded calls.
func (s *StubBillingClient) Calls() []PartitionArchivedInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PartitionArchivedInput(nil), s.calls...)
}
