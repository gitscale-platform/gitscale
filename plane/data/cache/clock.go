package cache

import "time"

// Clock is an injectable time source. The real implementation uses the system
// clock; tests inject a fake that can be advanced deterministically.
type Clock interface {
	Now() time.Time
}

// RealClock returns actual wall-clock time.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
