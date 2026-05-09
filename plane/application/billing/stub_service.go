package billing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// StubService is an in-memory Service for unit tests. It enforces the same
// idempotency contract as PostgresService — the natural key
// (year, month, partition_name) is unique and a duplicate insert returns the
// existing archive id with Created=false.
type StubService struct {
	mu        sync.Mutex
	rows      map[string]PartitionArchive
	dekEvents map[uuid.UUID]struct{}
	ciJobs    map[uuid.UUID]struct{}              // emitted JobIDs
	quotas    map[uuid.UUID]GetQuotaAccountOutput // by org id
	clock     func() time.Time
}

// NewStubService returns an empty StubService backed by time.Now().UTC().
func NewStubService() *StubService {
	return &StubService{
		rows:      map[string]PartitionArchive{},
		dekEvents: map[uuid.UUID]struct{}{},
		ciJobs:    map[uuid.UUID]struct{}{},
		quotas:    map[uuid.UUID]GetQuotaAccountOutput{},
		clock:     func() time.Time { return time.Now().UTC() },
	}
}

// SeedQuota installs a quota response for orgID. Test helper.
func (s *StubService) SeedQuota(orgID uuid.UUID, q GetQuotaAccountOutput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotas[orgID] = q
}

// GetQuotaAccount returns the seeded quota or ErrQuotaAccountNotFound.
func (s *StubService) GetQuotaAccount(_ context.Context, in GetQuotaAccountInput) (GetQuotaAccountOutput, error) {
	if in.OrgID == uuid.Nil {
		return GetQuotaAccountOutput{}, ErrEmptyOrgID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.quotas[in.OrgID]
	if !ok {
		return GetQuotaAccountOutput{}, ErrQuotaAccountNotFound
	}
	return q, nil
}

// RecordCIJobCompleted records the JobID and returns Created=false on
// retry. Mirrors the postgres impl's idempotency contract.
func (s *StubService) RecordCIJobCompleted(_ context.Context, in RecordCIJobCompletedInput) (RecordCIJobCompletedOutput, error) {
	if err := validateCIJobInput(in); err != nil {
		return RecordCIJobCompletedOutput{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := ciJobCompletedAggregateID(in.JobID)
	if _, ok := s.ciJobs[id]; ok {
		return RecordCIJobCompletedOutput{EventID: id.String(), Created: false}, nil
	}
	s.ciJobs[id] = struct{}{}
	return RecordCIJobCompletedOutput{EventID: id.String(), Created: true}, nil
}

// RecordPartitionArchived applies validation, then either creates a new
// archive entry or returns the existing one's id idempotently.
func (s *StubService) RecordPartitionArchived(_ context.Context, in RecordPartitionArchivedInput) (RecordPartitionArchivedOutput, error) {
	if err := validateInput(in); err != nil {
		return RecordPartitionArchivedOutput{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%d-%d-%s", in.Year, in.Month, in.PartitionName)
	if existing, ok := s.rows[key]; ok {
		return RecordPartitionArchivedOutput{ArchiveID: existing.ID.String(), Created: false}, nil
	}
	pa := PartitionArchive{
		ID:            uuid.New(),
		Year:          in.Year,
		Month:         in.Month,
		PartitionName: in.PartitionName,
		LakeURI:       in.LakeURI,
		RowCount:      in.RowCount,
		BytesWritten:  in.BytesWritten,
		ArchivedAt:    s.clock(),
	}
	s.rows[key] = pa
	return RecordPartitionArchivedOutput{ArchiveID: pa.ID.String(), Created: true}, nil
}

// RecordDEKDestroyed simulates the DEK-destruction outbox emit with the same
// idempotency contract as PostgresService — repeated calls with the same
// natural key return Created=false.
func (s *StubService) RecordDEKDestroyed(_ context.Context, in RecordDEKDestroyedInput) (RecordDEKDestroyedOutput, error) {
	if err := validateDEKDestroyedInput(in); err != nil {
		return RecordDEKDestroyedOutput{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := dekDestroyedAggregateID(in)
	if _, ok := s.dekEvents[id]; ok {
		return RecordDEKDestroyedOutput{EventID: id.String(), Created: false}, nil
	}
	s.dekEvents[id] = struct{}{}
	return RecordDEKDestroyedOutput{EventID: id.String(), Created: true}, nil
}
