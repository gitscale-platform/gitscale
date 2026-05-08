package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	stubstore "github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/google/uuid"
)

type fakeKEKResolver struct {
	hints map[string]string
	errs  map[string]error
}

func (f *fakeKEKResolver) ResolveKEKHint(_ context.Context, lakeURI string) (string, error) {
	if err, ok := f.errs[lakeURI]; ok {
		return "", err
	}
	return f.hints[lakeURI], nil
}

func TestListEligiblePartitionsActivity_FiltersByCutoff_AndResolvesHints(t *testing.T) {
	ms := stubstore.New()
	cutoff := time.Date(2034, 6, 1, 0, 0, 0, 0, time.UTC)

	// Seed the in-memory store with 2 old + 1 new partition archive. Use
	// the writer path to mirror production semantics.
	old1ID, old2ID, recentID := uuid.New(), uuid.New(), uuid.New()
	if err := ms.Transact(context.Background(), func(tx store.Tx) error {
		_, _, err := tx.Billing().InsertPartitionArchiveIfAbsent(context.Background(), store.PartitionArchive{
			ID: old1ID, Year: 2027, Month: 1, PartitionName: "p1",
			LakeURI: "s3://b/p1.parquet", ArchivedAt: cutoff.AddDate(0, 0, -10),
		})
		if err != nil {
			return err
		}
		_, _, err = tx.Billing().InsertPartitionArchiveIfAbsent(context.Background(), store.PartitionArchive{
			ID: old2ID, Year: 2027, Month: 2, PartitionName: "p2",
			LakeURI: "s3://b/p2.parquet", ArchivedAt: cutoff.AddDate(0, 0, -5),
		})
		if err != nil {
			return err
		}
		_, _, err = tx.Billing().InsertPartitionArchiveIfAbsent(context.Background(), store.PartitionArchive{
			ID: recentID, Year: 2034, Month: 5, PartitionName: "recent",
			LakeURI: "s3://b/recent.parquet", ArchivedAt: cutoff.AddDate(0, 0, 5),
		})
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resolver := &fakeKEKResolver{
		hints: map[string]string{
			"s3://b/p1.parquet": "platform-billing-v1",
			"s3://b/p2.parquet": "", // simulates resolution failure → empty hint, workflow skips
		},
		errs: map[string]error{
			"s3://b/p2.parquet": errors.New("manifest 404"),
		},
	}

	a, err := NewListEligiblePartitionsActivity(ms, resolver)
	if err != nil {
		t.Fatalf("NewListEligiblePartitionsActivity: %v", err)
	}
	res, err := a.Execute(context.Background(), ListEligibleInput{Cutoff: cutoff})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Partitions) != 2 {
		t.Fatalf("Partitions=%d want 2 (recent excluded)", len(res.Partitions))
	}
	if res.Partitions[0].KEKHint != "platform-billing-v1" {
		t.Errorf("p1 KEKHint=%q want platform-billing-v1", res.Partitions[0].KEKHint)
	}
	if res.Partitions[1].KEKHint != "" {
		t.Errorf("p2 KEKHint=%q want empty (resolver error → skip path)", res.Partitions[1].KEKHint)
	}
	if res.Partitions[0].Year != 2027 || res.Partitions[0].Month != 1 {
		t.Errorf("ordering broken: first=%v", res.Partitions[0])
	}
}

func TestListEligiblePartitionsActivity_ZeroCutoffRejected(t *testing.T) {
	ms := stubstore.New()
	a, _ := NewListEligiblePartitionsActivity(ms, &fakeKEKResolver{})
	_, err := a.Execute(context.Background(), ListEligibleInput{})
	if err == nil {
		t.Error("expected error for zero cutoff")
	}
}

func TestNewListEligiblePartitionsActivity_NilDeps(t *testing.T) {
	if _, err := NewListEligiblePartitionsActivity(nil, &fakeKEKResolver{}); err == nil {
		t.Error("expected error for nil store")
	}
	if _, err := NewListEligiblePartitionsActivity(stubstore.New(), nil); err == nil {
		t.Error("expected error for nil resolver")
	}
}
