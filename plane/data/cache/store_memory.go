package cache

import (
	"bytes"
	"context"
	"sync"
	"time"
)

type memEntry struct {
	value     []byte
	expiresAt time.Time
}

// MemoryStore is an in-process CacheStore for tests and local development.
// It accepts an injectable Clock so that TTL expiry can be driven
// deterministically in tests without real sleeps.
type MemoryStore struct {
	mu      sync.Mutex
	entries map[string]memEntry
	clock   Clock
}

// NewMemoryStore returns a CacheStore backed by an in-process map.
// Pass a FakeClock (or any Clock implementation) to control time in tests.
func NewMemoryStore(clk Clock) *MemoryStore {
	if clk == nil {
		clk = RealClock{}
	}
	return &MemoryStore{
		entries: make(map[string]memEntry),
		clock:   clk,
	}
}

func (m *MemoryStore) Get(ctx context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || m.clock.Now().After(e.expiresAt) {
		delete(m.entries, key)
		return nil, ErrNotFound
	}
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, nil
}

func (m *MemoryStore) MGet(ctx context.Context, keys []string) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	out := make([][]byte, len(keys))
	for i, key := range keys {
		e, ok := m.entries[key]
		if !ok || now.After(e.expiresAt) {
			delete(m.entries, key)
			out[i] = nil
			continue
		}
		cp := make([]byte, len(e.value))
		copy(cp, e.value)
		out[i] = cp
	}
	return out, nil
}

func (m *MemoryStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	m.entries[key] = memEntry{
		value:     cp,
		expiresAt: m.clock.Now().Add(ttl),
	}
	return nil
}

func (m *MemoryStore) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
	return nil
}

func (m *MemoryStore) CompareAndSwap(ctx context.Context, key string, expected, replacement []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()

	var cur []byte
	if e, ok := m.entries[key]; ok && !now.After(e.expiresAt) {
		cur = e.value
	}

	// "" expected means "key must be absent"; nil expected from caller also maps to absent.
	var exp []byte
	if len(expected) > 0 {
		exp = expected
	}
	if !bytes.Equal(cur, exp) {
		return false, nil
	}

	cp := make([]byte, len(replacement))
	copy(cp, replacement)
	m.entries[key] = memEntry{
		value:     cp,
		expiresAt: now.Add(ttl),
	}
	return true, nil
}

func (m *MemoryStore) Ping(_ context.Context) error {
	return nil
}

// FakeClock is a Clock whose current time can be advanced manually.
// Safe for concurrent use from multiple goroutines.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock returns a FakeClock starting at t.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: t}
}

func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the clock forward by d.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// ensure MemoryStore satisfies CacheStore at compile time.
var _ CacheStore = (*MemoryStore)(nil)

// ensure FakeClock satisfies Clock at compile time.
var _ Clock = (*FakeClock)(nil)

