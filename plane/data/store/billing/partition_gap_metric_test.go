package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"github.com/prometheus/client_golang/prometheus"
)

type fakeProbe struct {
	upperBound time.Time
	count      int
}

func (f fakeProbe) MaxPartitionUpperBound(_ context.Context) (time.Time, int, error) {
	return f.upperBound, f.count, nil
}

func TestPartitionGapMetric_RefreshComputesDaysAndCount(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	m := billing.NewPartitionGapMetricWithProbe(fakeProbe{
		upperBound: time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC),
		count:      12,
	}, reg, func() time.Time { return time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC) })

	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readGauge(t, reg, "gitscale_billing_partition_days_until_gap"); got != 16 {
		t.Fatalf("days_until_gap=%v want 16", got)
	}
	if got := readGauge(t, reg, "gitscale_billing_partition_count"); got != 12 {
		t.Fatalf("partition_count=%v want 12", got)
	}
}

func readGauge(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %s not present", name)
	return 0
}
