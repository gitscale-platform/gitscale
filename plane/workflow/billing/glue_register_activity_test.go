package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
)

type fakeGlueClient struct {
	calls []*glue.CreatePartitionInput
	err   error
}

func (f *fakeGlueClient) CreatePartition(ctx context.Context, in *glue.CreatePartitionInput, opts ...func(*glue.Options)) (*glue.CreatePartitionOutput, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return nil, f.err
	}
	return &glue.CreatePartitionOutput{}, nil
}

func TestGlueRegisterActivity_NewValidatesArgs(t *testing.T) {
	if _, err := NewGlueRegisterActivity(nil, "db", "tbl"); err == nil {
		t.Error("nil client should error")
	}
	fc := &fakeGlueClient{}
	if _, err := NewGlueRegisterActivity(fc, "", "tbl"); err == nil {
		t.Error("empty database should error")
	}
	if _, err := NewGlueRegisterActivity(fc, "db", ""); err == nil {
		t.Error("empty table should error")
	}
	if _, err := NewGlueRegisterActivity(fc, "db", "tbl"); err != nil {
		t.Errorf("valid args: %v", err)
	}
}

func TestGlueRegisterActivity_HappyPath(t *testing.T) {
	fc := &fakeGlueClient{}
	a, err := NewGlueRegisterActivity(fc, "gitscale_analytics", "usage_events")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	in := GlueRegisterInput{
		Year:    2026,
		Month:   5,
		LakeURI: "s3://bucket/billing/usage_events/year=2026/month=05/",
	}
	if err := a.Execute(context.Background(), in); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fc.calls) != 1 {
		t.Fatalf("calls=%d want 1", len(fc.calls))
	}
	got := fc.calls[0]
	if *got.DatabaseName != "gitscale_analytics" {
		t.Errorf("db=%q", *got.DatabaseName)
	}
	if *got.TableName != "usage_events" {
		t.Errorf("table=%q", *got.TableName)
	}
	pi := got.PartitionInput
	if len(pi.Values) != 2 || pi.Values[0] != "2026" || pi.Values[1] != "05" {
		t.Errorf("values=%v want [2026 05]", pi.Values)
	}
	if *pi.StorageDescriptor.Location != in.LakeURI {
		t.Errorf("location=%q want %q", *pi.StorageDescriptor.Location, in.LakeURI)
	}
	if *pi.StorageDescriptor.InputFormat != parquetInputFormat {
		t.Errorf("inputFormat=%q", *pi.StorageDescriptor.InputFormat)
	}
	if *pi.StorageDescriptor.OutputFormat != parquetOutputFormat {
		t.Errorf("outputFormat=%q", *pi.StorageDescriptor.OutputFormat)
	}
	if *pi.StorageDescriptor.SerdeInfo.SerializationLibrary != parquetSerde {
		t.Errorf("serde=%q", *pi.StorageDescriptor.SerdeInfo.SerializationLibrary)
	}
}

func TestGlueRegisterActivity_AlreadyExistsIsIdempotent(t *testing.T) {
	fc := &fakeGlueClient{err: &gluetypes.AlreadyExistsException{Message: stringPtr("partition exists")}}
	a, err := NewGlueRegisterActivity(fc, "db", "tbl")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	in := GlueRegisterInput{Year: 2026, Month: 5, LakeURI: "s3://b/k/"}
	if err := a.Execute(context.Background(), in); err != nil {
		t.Errorf("AlreadyExists should be swallowed; got %v", err)
	}
}

func TestGlueRegisterActivity_OtherErrorPropagates(t *testing.T) {
	fc := &fakeGlueClient{err: errors.New("boom")}
	a, err := NewGlueRegisterActivity(fc, "db", "tbl")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	in := GlueRegisterInput{Year: 2026, Month: 5, LakeURI: "s3://b/k/"}
	if err := a.Execute(context.Background(), in); err == nil {
		t.Error("expected error to propagate")
	}
}

func TestGlueRegisterActivity_ValidatesInput(t *testing.T) {
	fc := &fakeGlueClient{}
	a, _ := NewGlueRegisterActivity(fc, "db", "tbl")
	cases := []struct {
		name string
		in   GlueRegisterInput
	}{
		{"yearLow", GlueRegisterInput{Year: 2025, Month: 5, LakeURI: "s3://b/k/"}},
		{"yearHigh", GlueRegisterInput{Year: 2100, Month: 5, LakeURI: "s3://b/k/"}},
		{"monthZero", GlueRegisterInput{Year: 2026, Month: 0, LakeURI: "s3://b/k/"}},
		{"monthThirteen", GlueRegisterInput{Year: 2026, Month: 13, LakeURI: "s3://b/k/"}},
		{"emptyURI", GlueRegisterInput{Year: 2026, Month: 5, LakeURI: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := a.Execute(context.Background(), tc.in); err == nil {
				t.Errorf("expected error for %+v", tc.in)
			}
		})
	}
	if len(fc.calls) != 0 {
		t.Errorf("invalid inputs should not reach SDK: calls=%d", len(fc.calls))
	}
}

func stringPtr(s string) *string { return &s }
