package billing

import (
	"context"
	"errors"
	"testing"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

func TestDetachPartitionActivity_callsArchiver(t *testing.T) {
	stub := billingstore.NewStubArchiver()
	act, err := NewDetachPartitionActivity(stub)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	err = act.Execute(ctx, DetachInput{Year: 2026, Month: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !stub.IsDetached(2026, 5) {
		t.Error("expected partition to be detached")
	}
}

func TestDetachPartitionActivity_propagatesError(t *testing.T) {
	stub := billingstore.NewStubArchiver()
	stub.DetachFn = func(year, month int) error {
		return errors.New("pg: connection reset")
	}
	act, _ := NewDetachPartitionActivity(stub)

	err := act.Execute(context.Background(), DetachInput{Year: 2026, Month: 5})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestNewDetachPartitionActivity_nilArchiver(t *testing.T) {
	if _, err := NewDetachPartitionActivity(nil); err == nil {
		t.Error("expected error for nil archiver")
	}
}
