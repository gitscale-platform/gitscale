package billing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/billing"
)

func validInput() billing.RecordPartitionArchivedInput {
	return billing.RecordPartitionArchivedInput{
		Year:          2026,
		Month:         5,
		PartitionName: "usage_events_2026_05",
		LakeURI:       "s3://lake/usage/2026/05/",
		RowCount:      1,
		BytesWritten:  100,
	}
}

func TestStubService_FirstCallCreates(t *testing.T) {
	s := billing.NewStubService()
	out, err := s.RecordPartitionArchived(context.Background(), validInput())
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !out.Created {
		t.Fatalf("expected Created=true, got false")
	}
	if out.ArchiveID == "" {
		t.Fatalf("expected non-empty ArchiveID")
	}
}

func TestStubService_RetryIsIdempotent(t *testing.T) {
	s := billing.NewStubService()
	in := validInput()
	first, err := s.RecordPartitionArchived(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.RecordPartitionArchived(context.Background(), in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Created {
		t.Fatalf("expected Created=false on retry")
	}
	if second.ArchiveID != first.ArchiveID {
		t.Fatalf("expected same ArchiveID, got %s vs %s", second.ArchiveID, first.ArchiveID)
	}
}

func TestStubService_ValidationErrors(t *testing.T) {
	s := billing.NewStubService()
	cases := []struct {
		name  string
		mut   func(*billing.RecordPartitionArchivedInput)
		wantE error
	}{
		{"year-low", func(i *billing.RecordPartitionArchivedInput) { i.Year = 2025 }, billing.ErrInvalidYear},
		{"year-high", func(i *billing.RecordPartitionArchivedInput) { i.Year = 2101 }, billing.ErrInvalidYear},
		{"month-zero", func(i *billing.RecordPartitionArchivedInput) { i.Month = 0 }, billing.ErrInvalidMonth},
		{"month-thirteen", func(i *billing.RecordPartitionArchivedInput) { i.Month = 13 }, billing.ErrInvalidMonth},
		{"empty-name", func(i *billing.RecordPartitionArchivedInput) { i.PartitionName = "" }, billing.ErrEmptyPartitionName},
		{"empty-uri", func(i *billing.RecordPartitionArchivedInput) { i.LakeURI = "" }, billing.ErrEmptyLakeURI},
		{"negative-rows", func(i *billing.RecordPartitionArchivedInput) { i.RowCount = -1 }, billing.ErrNegativeCount},
		{"negative-bytes", func(i *billing.RecordPartitionArchivedInput) { i.BytesWritten = -1 }, billing.ErrNegativeCount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mut(&in)
			_, err := s.RecordPartitionArchived(context.Background(), in)
			if !errors.Is(err, tc.wantE) {
				t.Fatalf("want %v got %v", tc.wantE, err)
			}
		})
	}
}
