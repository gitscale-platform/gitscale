package billing

import (
	"testing"
	"time"
)

// TestStubArchiver_DetachAndDrop validates the stub used by workflow unit tests.
// The postgres impl is covered by the integration test (-tags integration).
func TestStubArchiver_DetachAndDrop(t *testing.T) {
	a := NewStubArchiver()
	ctx := t.Context()

	if err := a.DetachUsageEventsPartition(ctx, 2026, 5); err != nil {
		t.Fatal(err)
	}
	if !a.IsDetached(2026, 5) {
		t.Error("expected partition to be detached")
	}

	if err := a.DropUsageEventsPartition(ctx, 2026, 5); err != nil {
		t.Fatal(err)
	}
	if !a.IsDropped(2026, 5) {
		t.Error("expected partition to be dropped")
	}
}

func TestStubArchiver_ScanRows(t *testing.T) {
	a := NewStubArchiver()
	ts := mustParseTime("2026-05-15T10:00:00Z")
	a.SetRows(2026, 5, []UsageEventRow{
		SeedUsageEventRow("id-1", "acc-1", ts),
		SeedUsageEventRow("id-2", "acc-1", ts),
	})

	ctx := t.Context()
	cur, err := a.ScanPartitionRows(ctx, 2026, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close()

	var count int
	for cur.Next(ctx) {
		count++
		_ = cur.Row()
	}
	if cur.Err() != nil {
		t.Fatal(cur.Err())
	}
	if count != 2 {
		t.Errorf("got %d rows, want 2", count)
	}
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
