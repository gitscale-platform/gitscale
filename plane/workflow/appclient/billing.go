package appclient

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
)

// BillingClient is the workflow-plane view of the application-plane billing
// service. State-mutating calls correspond to gRPC unary calls that write
// the source row + outbox row in a single transaction (ADR-008 + ADR-019).
type BillingClient interface {
	// RecordPartitionArchived emits billing.partition_archived to the outbox
	// via the billing app-plane service.
	RecordPartitionArchived(ctx context.Context, in PartitionArchivedInput) error

	// RecordDEKDestroyed emits billing.partition_dek_destroyed to the outbox
	// via the billing app-plane service. Idempotent on the natural key
	// (year, month, partition_name, kek_hint).
	RecordDEKDestroyed(ctx context.Context, in DEKDestroyedInput) error

	// GetQuotaAccount fetches the per-org quota envelope (#110, ADR-019).
	// Lookup is by org id (already resolved at the trigger entrypoint —
	// REST API #111). Returns ErrQuotaAccountNotFound when the org has no
	// quota row; boot activities classify that as non-retryable.
	GetQuotaAccount(ctx context.Context, orgID uuid.UUID) (QuotaAccountView, error)

	// RecordCIJobCompleted emits ci.job_completed to the outbox via the
	// billing app-plane service (#110, ADR-008/019). Idempotent on JobID.
	RecordCIJobCompleted(ctx context.Context, in CIJobCompletedInput) error
}

// QuotaAccountView is the workflow-plane projection of a billing.quota_accounts
// row. Caps are per-period; the boot activity uses ComputeMinutesPerMonthCap
// as the per-job ceiling derivation source.
type QuotaAccountView struct {
	AccountID                 uuid.UUID
	OrgID                     uuid.UUID
	PlanTier                  string
	TokensPerWeekCap          int64
	ComputeMinutesPerMonthCap int64
	StorageGBCap              int64
}

// CIJobCompletedInput is the workflow-plane projection of the
// ci.job_completed outbox payload (#110). The application-plane service
// writes the source row + outbox row in one Tx (ADR-008).
type CIJobCompletedInput struct {
	JobID           uuid.UUID
	PrincipalID     uuid.UUID
	PrincipalKind   string // "human" | "agent" | "service"
	OrgID           uuid.UUID
	RepoID          uuid.UUID
	Tier            string // "hot" | "cold"
	VCPUSeconds     float64
	MemoryMBSeconds float64
	EgressKB        int64
	ExitCode        int
}

// ErrQuotaAccountNotFound is returned by GetQuotaAccount when the principal's
// organisation has no billing.quota_accounts row. Boot activities classify
// this as non-retryable.
var ErrQuotaAccountNotFound = errors.New("appclient: quota account not found")

// DEKDestroyedInput carries the destruction outcome to the billing service.
// VaultKeyVersion is the parsed numeric N from "platform-billing-v<N>" and
// must be > 0; KEKHint is preserved verbatim for audit trail.
type DEKDestroyedInput struct {
	Year            int
	Month           int
	PartitionName   string
	KEKHint         string
	VaultKeyVersion int
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
	mu       sync.Mutex
	calls    []PartitionArchivedInput
	dekCalls []DEKDestroyedInput
	fn       func(in PartitionArchivedInput) error
	dekFn    func(in DEKDestroyedInput) error

	// CI/quota stubs (#110).
	ciCalls    []CIJobCompletedInput
	ciFn       func(in CIJobCompletedInput) error
	quota      map[uuid.UUID]QuotaAccountView // keyed by orgID
	quotaErrFn func(orgID uuid.UUID) error
}

// NewStubBillingClient returns a recording stub that succeeds by default.
func NewStubBillingClient() *StubBillingClient { return &StubBillingClient{} }

// SetFn injects a fake-error path.
func (s *StubBillingClient) SetFn(fn func(PartitionArchivedInput) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn = fn
}

// SetDEKFn injects a fake-error path for RecordDEKDestroyed.
func (s *StubBillingClient) SetDEKFn(fn func(DEKDestroyedInput) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dekFn = fn
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

// RecordDEKDestroyed records the DEK-destruction call and runs the injected
// fn if set.
func (s *StubBillingClient) RecordDEKDestroyed(_ context.Context, in DEKDestroyedInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dekFn != nil {
		if err := s.dekFn(in); err != nil {
			return err
		}
	}
	s.dekCalls = append(s.dekCalls, in)
	return nil
}

// Calls returns a snapshot of all recorded archive calls.
func (s *StubBillingClient) Calls() []PartitionArchivedInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PartitionArchivedInput(nil), s.calls...)
}

// DEKCalls returns a snapshot of all recorded DEK-destruction calls.
func (s *StubBillingClient) DEKCalls() []DEKDestroyedInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DEKDestroyedInput(nil), s.dekCalls...)
}

// SetCIFn injects a fake-error path for RecordCIJobCompleted.
func (s *StubBillingClient) SetCIFn(fn func(CIJobCompletedInput) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ciFn = fn
}

// SetQuotaFor pre-populates the quota response for an org.
func (s *StubBillingClient) SetQuotaFor(orgID uuid.UUID, q QuotaAccountView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quota == nil {
		s.quota = make(map[uuid.UUID]QuotaAccountView)
	}
	s.quota[orgID] = q
}

// SetQuotaErr installs a function that returns an error for selected
// orgs. Returning nil falls through to the SetQuotaFor map.
func (s *StubBillingClient) SetQuotaErr(fn func(uuid.UUID) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotaErrFn = fn
}

// GetQuotaAccount implements BillingClient.
func (s *StubBillingClient) GetQuotaAccount(_ context.Context, orgID uuid.UUID) (QuotaAccountView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quotaErrFn != nil {
		if err := s.quotaErrFn(orgID); err != nil {
			return QuotaAccountView{}, err
		}
	}
	if s.quota == nil {
		return QuotaAccountView{}, ErrQuotaAccountNotFound
	}
	q, ok := s.quota[orgID]
	if !ok {
		return QuotaAccountView{}, ErrQuotaAccountNotFound
	}
	return q, nil
}

// RecordCIJobCompleted implements BillingClient.
func (s *StubBillingClient) RecordCIJobCompleted(_ context.Context, in CIJobCompletedInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ciFn != nil {
		if err := s.ciFn(in); err != nil {
			return err
		}
	}
	s.ciCalls = append(s.ciCalls, in)
	return nil
}

// CICalls returns a snapshot of all recorded CI-job-completion calls.
func (s *StubBillingClient) CICalls() []CIJobCompletedInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CIJobCompletedInput(nil), s.ciCalls...)
}
