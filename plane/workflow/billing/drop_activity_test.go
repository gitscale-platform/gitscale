package billing

import (
	"context"
	"errors"
	"testing"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

func TestDropPartitionActivity_callsArchiver(t *testing.T) {
	stub := billingstore.NewStubArchiver()
	act, err := NewDropPartitionActivity(stub)
	if err != nil {
		t.Fatal(err)
	}

	if err := act.Execute(context.Background(), DropInput{Year: 2026, Month: 5}); err != nil {
		t.Fatal(err)
	}
	if !stub.IsDropped(2026, 5) {
		t.Error("expected partition to be dropped")
	}
}

func TestDropPartitionActivity_propagatesError(t *testing.T) {
	stub := billingstore.NewStubArchiver()
	stub.DropFn = func(_, _ int) error { return errors.New("pg: permission denied") }
	act, _ := NewDropPartitionActivity(stub)

	if err := act.Execute(context.Background(), DropInput{Year: 2026, Month: 5}); err == nil {
		t.Error("expected error, got nil")
	}
}

func TestNewDropPartitionActivity_nilArchiver(t *testing.T) {
	if _, err := NewDropPartitionActivity(nil); err == nil {
		t.Error("expected error for nil archiver")
	}
}
