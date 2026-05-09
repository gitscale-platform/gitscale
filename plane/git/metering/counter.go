// Package metering carries the metering Counter contract used by the proxy
// to record per-RPC events. This package ships a no-op implementation only;
// the production two-tier counter (Redis enforcement + outbox reconciliation)
// lands in #109.
package metering

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/git/gittypes"
)

// Counter records a metering event for a completed Git operation.
// op is one of "info_refs", "upload_pack", "receive_pack". bytes is the
// payload size in either direction; packObjects and refUpdates are
// receive-pack specific (zero on fetch paths).
//
// Implementations must be safe for concurrent use. A non-nil error from
// Record is propagated by the proxy and rejects the operation; the production
// implementation distinguishes best-effort enforcement (Redis) from
// load-bearing reconciliation (outbox).
type Counter interface {
	Record(ctx context.Context, ref gittypes.RepoRef, op string, bytes int64, packObjects int64, refUpdates int) error
}

// noopCounter discards all events. Returned by NewNoopCounter.
type noopCounter struct{}

// NewNoopCounter returns a Counter that drops every event. Used as the
// default in tests and during bootstrap before the two-tier counter
// (#109) wires in.
func NewNoopCounter() Counter { return noopCounter{} }

func (noopCounter) Record(_ context.Context, _ gittypes.RepoRef, _ string, _ int64, _ int64, _ int) error {
	return nil
}
