package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

func TestEmitArchiveEventActivity_callsBillingClient(t *testing.T) {
	stub := appclient.NewStubBillingClient()
	act, err := NewEmitArchiveEventActivity(stub)
	if err != nil {
		t.Fatal(err)
	}

	in := EmitInput{
		Year:          2026,
		Month:         5,
		PartitionName: "billing.usage_events_2026_05",
		LakeURI:       "s3://gitscale-analytics-test/billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet",
		RowCount:      1000,
		BytesWritten:  5000,
	}
	if err := act.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}

	calls := stub.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(calls))
	}
	got := calls[0]
	if got.LakeURI != in.LakeURI {
		t.Errorf("LakeURI=%s want %s", got.LakeURI, in.LakeURI)
	}
	if got.RowCount != in.RowCount {
		t.Errorf("RowCount=%d want %d", got.RowCount, in.RowCount)
	}
}

func TestEmitArchiveEventActivity_propagatesError(t *testing.T) {
	stub := appclient.NewStubBillingClient()
	stub.SetFn(func(_ appclient.PartitionArchivedInput) error {
		return errors.New("grpc: unavailable")
	})
	act, _ := NewEmitArchiveEventActivity(stub)

	err := act.Execute(context.Background(), EmitInput{Year: 2026, Month: 5})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestNewEmitArchiveEventActivity_nilClient(t *testing.T) {
	if _, err := NewEmitArchiveEventActivity(nil); err == nil {
		t.Error("expected error for nil client")
	}
}
