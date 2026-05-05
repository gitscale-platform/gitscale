package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Clock is an injectable time source for the memory limiter.
type Clock interface {
	Now() time.Time
}

// RealClock returns actual wall-clock time.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

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

type bucketState struct {
	tokens float64
	lastAt time.Time
}

// MemoryLimiter implements RateLimiter with in-process state.
// Each bucket is a struct protected by a per-key mutex (coarser: single map mutex).
// Accepts an injectable Clock for deterministic tests.
type MemoryLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucketState
	clock   Clock
}

// NewMemoryLimiter returns a RateLimiter backed by an in-process map.
func NewMemoryLimiter(clk Clock) *MemoryLimiter {
	if clk == nil {
		clk = RealClock{}
	}
	return &MemoryLimiter{
		buckets: make(map[string]*bucketState),
		clock:   clk,
	}
}

func (m *MemoryLimiter) Take(_ context.Context, key string, capacity, refillPerSec, n float64) (bool, float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.clock.Now()
	b, ok := m.buckets[key]
	if !ok {
		b = &bucketState{tokens: capacity, lastAt: now}
		m.buckets[key] = b
	}

	elapsed := now.Sub(b.lastAt).Seconds()
	b.tokens += elapsed * refillPerSec
	if b.tokens > capacity {
		b.tokens = capacity
	}
	b.lastAt = now

	if b.tokens < n {
		return false, b.tokens, nil
	}
	b.tokens -= n
	return true, b.tokens, nil
}

// ensure MemoryLimiter satisfies RateLimiter at compile time.
var _ RateLimiter = (*MemoryLimiter)(nil)

// ensure FakeClock satisfies Clock at compile time.
var _ Clock = (*FakeClock)(nil)
