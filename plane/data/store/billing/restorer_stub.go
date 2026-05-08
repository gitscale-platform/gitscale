// plane/data/store/billing/restorer_stub.go
package billing

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// StubRestorer is an in-memory Restorer for activity unit tests. It records
// each call so tests can assert order without touching Postgres.
type StubRestorer struct {
	mu             sync.Mutex
	created        map[string]bool
	sealed         map[string]bool
	dropped        map[string]bool
	loadedRows     map[string]int64
	rowReader      func(year, month int, r io.Reader) (int64, error)
	EnsureFn       func(year, month int) error
	LoadFn         func(year, month int, r io.Reader) (int64, error)
	SealFn         func(year, month int) error
	DropFn         func(year, month int) error
}

// NewStubRestorer returns a StubRestorer with empty state.
func NewStubRestorer() *StubRestorer {
	return &StubRestorer{
		created:    map[string]bool{},
		sealed:     map[string]bool{},
		dropped:    map[string]bool{},
		loadedRows: map[string]int64{},
	}
}

// SetRowReader installs a function that reads `r` and reports rowcount.
// Tests use this to verify decoded plaintext (e.g. assert known length).
func (s *StubRestorer) SetRowReader(fn func(year, month int, r io.Reader) (int64, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rowReader = fn
}

func (s *StubRestorer) EnsureQuarantineTable(_ context.Context, year, month int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.EnsureFn != nil {
		if err := s.EnsureFn(year, month); err != nil {
			return "", err
		}
	}
	s.created[partitionKey(year, month)] = true
	return QuarantineTableName(year, month), nil
}

func (s *StubRestorer) LoadParquetIntoQuarantine(_ context.Context, year, month int, r io.Reader) (int64, error) {
	if s.LoadFn != nil {
		return s.LoadFn(year, month, r)
	}
	s.mu.Lock()
	rowReader := s.rowReader
	s.mu.Unlock()
	if rowReader != nil {
		n, err := rowReader(year, month, r)
		if err != nil {
			return 0, err
		}
		s.mu.Lock()
		s.loadedRows[partitionKey(year, month)] = n
		s.mu.Unlock()
		return n, nil
	}
	// Default: drain and count zero rows.
	if _, err := io.Copy(io.Discard, r); err != nil {
		return 0, fmt.Errorf("stub restorer: drain: %w", err)
	}
	return 0, nil
}

func (s *StubRestorer) SealQuarantineTable(_ context.Context, year, month int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SealFn != nil {
		if err := s.SealFn(year, month); err != nil {
			return err
		}
	}
	s.sealed[partitionKey(year, month)] = true
	return nil
}

func (s *StubRestorer) DropQuarantineTable(_ context.Context, year, month int) error {
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

// IsCreated reports whether EnsureQuarantineTable was called.
func (s *StubRestorer) IsCreated(year, month int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created[partitionKey(year, month)]
}

// IsSealed reports whether SealQuarantineTable was called.
func (s *StubRestorer) IsSealed(year, month int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sealed[partitionKey(year, month)]
}

// IsDropped reports whether DropQuarantineTable was called.
func (s *StubRestorer) IsDropped(year, month int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped[partitionKey(year, month)]
}

// LoadedRows returns the row count recorded for (year, month) after
// LoadParquetIntoQuarantine.
func (s *StubRestorer) LoadedRows(year, month int) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadedRows[partitionKey(year, month)]
}
