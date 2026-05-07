package billing

import (
	"context"
	"fmt"
	"sync"
)

// StubPartitioner records calls in-memory. Used by workflow unit tests and
// in dev environments without a Postgres dependency.
type StubPartitioner struct {
	mu       sync.Mutex
	created  map[string]int // partition_name → call count
	createFn func(year, month int) error
}

func NewStubPartitioner() *StubPartitioner {
	return &StubPartitioner{created: map[string]int{}}
}

// SetCreateFn injects a fake-error path for negative tests.
func (s *StubPartitioner) SetCreateFn(fn func(year, month int) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createFn = fn
}

// CreateUsageEventsPartition records the (year, month) call. First call with
// a given (year, month) returns created=true; subsequent calls return false.
func (s *StubPartitioner) CreateUsageEventsPartition(_ context.Context, year, month int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createFn != nil {
		if err := s.createFn(year, month); err != nil {
			return false, err
		}
	}
	key := fmt.Sprintf("usage_events_%04d_%02d", year, month)
	count := s.created[key]
	s.created[key]++
	return count == 0, nil
}

// Calls returns a snapshot of partition_name → call count.
func (s *StubPartitioner) Calls() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.created))
	for k, v := range s.created {
		out[k] = v
	}
	return out
}
