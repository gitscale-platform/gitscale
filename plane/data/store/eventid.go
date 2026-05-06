package store

import "github.com/google/uuid"

// NewEventID returns a new monotonic UUIDv7 suitable for use as an outbox
// event_id. UUIDv7 embeds a millisecond timestamp in the high bits, giving
// monotonic ordering within a process and low collision risk under concurrent
// inserts (unlike UUIDv4 which has no ordering guarantee).
func NewEventID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 only fails if the clock source fails; fall back to v4.
		return uuid.New()
	}
	return id
}
