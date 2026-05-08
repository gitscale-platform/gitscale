//go:build integration

package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"github.com/prometheus/client_golang/prometheus"
)

func TestPartitionGapMetric_IntegrationAgainstRealMigrations(t *testing.T) {
	ctx := context.Background()
	pool := setupPG(t)
	reg := prometheus.NewRegistry()

	m := billing.NewPartitionGapMetric(pool, reg)
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readGauge(t, reg, "gitscale_billing_partition_count"); got != 12 {
		t.Fatalf("count=%v want 12 (per 005_billing.sql)", got)
	}
	// Last seeded partition upper bound is 2027-05-01.
	expected := float64(int(time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC).Sub(time.Now().UTC()).Hours() / 24))
	if got := readGauge(t, reg, "gitscale_billing_partition_days_until_gap"); got != expected {
		t.Fatalf("days=%v want %v", got, expected)
	}
}
