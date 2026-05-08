// plane/data/store/billing/archiver_stub.go
package billing

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// StubArchiver is an in-memory Archiver for workflow unit tests.
type StubArchiver struct {
	mu         sync.Mutex
	detached   map[string]bool
	dropped    map[string]bool
	rows       map[string][]UsageEventRow
	lastCursor *stubCursor

	DetachFn func(year, month int) error
	DropFn   func(year, month int) error
}

// NewStubArchiver returns a StubArchiver with an empty row set. Use SetRows
// to inject test data before running an activity.
func NewStubArchiver() *StubArchiver {
	return &StubArchiver{
		detached: map[string]bool{},
		dropped:  map[string]bool{},
		rows:     map[string][]UsageEventRow{},
	}
}

// SetRows seeds the stub with rows for the given (year, month) partition.
func (s *StubArchiver) SetRows(year, month int, rows []UsageEventRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[partitionKey(year, month)] = rows
}

func (s *StubArchiver) DetachUsageEventsPartition(_ context.Context, year, month int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DetachFn != nil {
		if err := s.DetachFn(year, month); err != nil {
			return err
		}
	}
	s.detached[partitionKey(year, month)] = true
	return nil
}

func (s *StubArchiver) DropUsageEventsPartition(_ context.Context, year, month int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DropFn != nil {
		if err := s.DropFn(year, month); err != nil {
			return err
		}
	}
	s.dropped[partitionKey(year, month)] = true
	return nil
}

func (s *StubArchiver) ScanPartitionRows(_ context.Context, year, month int) (RowCursor, error) {
	s.mu.Lock()
	rows := append([]UsageEventRow(nil), s.rows[partitionKey(year, month)]...)
	c := &stubCursor{rows: rows}
	s.lastCursor = c
	s.mu.Unlock()
	return c, nil
}

// LastCursorCloses returns the number of times Close() was called on the most
// recently issued stubCursor, or 0 if no cursor has been issued.
func (s *StubArchiver) LastCursorCloses() int {
	s.mu.Lock()
	c := s.lastCursor
	s.mu.Unlock()
	if c == nil {
		return 0
	}
	return c.Closes()
}

// IsDetached reports whether DetachUsageEventsPartition was called for (year, month).
func (s *StubArchiver) IsDetached(year, month int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached[partitionKey(year, month)]
}

// IsDropped reports whether DropUsageEventsPartition was called for (year, month).
func (s *StubArchiver) IsDropped(year, month int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped[partitionKey(year, month)]
}

type stubCursor struct {
	rows []UsageEventRow
	pos  int
	cur  UsageEventRow

	mu     sync.Mutex
	closes int
}

func (c *stubCursor) Next(_ context.Context) bool {
	if c.pos >= len(c.rows) {
		return false
	}
	c.cur = c.rows[c.pos]
	c.pos++
	return true
}

func (c *stubCursor) Row() UsageEventRow { return c.cur }
func (c *stubCursor) Err() error         { return nil }
func (c *stubCursor) Close() error {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
	return nil
}

// Closes returns the number of times Close() has been called on this cursor.
func (c *stubCursor) Closes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

func partitionKey(year, month int) string {
	return fmt.Sprintf("%04d_%02d", year, month)
}

// SeedUsageEventRow returns a representative UsageEventRow for a given timestamp.
// Used by tests to build deterministic fixtures.
func SeedUsageEventRow(id, accountID string, ts time.Time) UsageEventRow {
	return UsageEventRow{
		ID:            id,
		AccountID:     accountID,
		PrincipalID:   "00000000-0000-0000-0000-000000000001",
		PrincipalType: "agent",
		Surface:       "tokens",
		CostVector:    `{"model":"claude-sonnet-4-6"}`,
		Value:         1000,
		EventSource:   "api",
		Ts:            ts,
		CreatedAt:     ts,
	}
}
