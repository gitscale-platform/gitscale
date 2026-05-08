package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

func TestEmitDEKDestroyedActivity_ForwardsToBillingClient(t *testing.T) {
	stub := appclient.NewStubBillingClient()
	a, err := NewEmitDEKDestroyedActivity(stub)
	if err != nil {
		t.Fatalf("NewEmitDEKDestroyedActivity: %v", err)
	}
	in := EmitDEKDestroyedInput{
		Year: 2027, Month: 1, PartitionName: "p1",
		KEKHint: "platform-billing-v1", VaultKeyVersion: 1,
	}
	if err := a.Execute(context.Background(), in); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	calls := stub.DEKCalls()
	if len(calls) != 1 {
		t.Fatalf("DEKCalls=%d want 1", len(calls))
	}
	if calls[0].KEKHint != "platform-billing-v1" || calls[0].VaultKeyVersion != 1 {
		t.Errorf("call payload mismatch: %+v", calls[0])
	}
}

func TestEmitDEKDestroyedActivity_NilClientRejected(t *testing.T) {
	if _, err := NewEmitDEKDestroyedActivity(nil); err == nil {
		t.Error("expected error for nil client")
	}
}

func TestEmitDEKDestroyedActivity_PropagatesClientError(t *testing.T) {
	stub := appclient.NewStubBillingClient()
	stub.SetDEKFn(func(_ appclient.DEKDestroyedInput) error { return errors.New("rpc failed") })
	a, _ := NewEmitDEKDestroyedActivity(stub)
	if err := a.Execute(context.Background(), EmitDEKDestroyedInput{
		Year: 2027, Month: 1, PartitionName: "p1", KEKHint: "platform-billing-v1", VaultKeyVersion: 1,
	}); err == nil {
		t.Error("expected error to propagate")
	}
}
