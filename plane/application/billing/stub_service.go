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
	mu    sync.Mutex
	rows  map[string]PartitionArchive
	clock func() time.Time
}

// NewStubService returns an empty StubService backed by time.Now().UTC().
func NewStubService() *StubService {
	return &StubService{
		rows:  map[string]PartitionArchive{},
		clock: func() time.Time { return time.Now().UTC() },
	}
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
