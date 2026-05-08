package billing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func registerRestoreActivityStubs(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions(
		func(context.Context, FetchManifestInput) (FetchManifestResult, error) { return FetchManifestResult{}, nil },
		activity.RegisterOptions{Name: ActivityNameFetchManifest},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, VerifyChecksumInput) error { return nil },
		activity.RegisterOptions{Name: ActivityNameVerifyChecksum},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, DownloadDecryptInput) (DownloadDecryptResult, error) { return DownloadDecryptResult{}, nil },
		activity.RegisterOptions{Name: ActivityNameDownloadDecrypt},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, LoadQuarantineInput) (LoadQuarantineResult, error) {
			return LoadQuarantineResult{}, nil
		},
		activity.RegisterOptions{Name: ActivityNameLoadQuarantine},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, DropQuarantineInput) error { return nil },
		activity.RegisterOptions{Name: ActivityNameDropQuarantine},
	)
}

func happyManifest() FetchManifestResult {
	return FetchManifestResult{
		SourcePartition: "billing.usage_events_2026_05",
		RowCount:        2,
		BytesWritten:    4096,
		KEKHint:         "platform-billing-v3",
		EncFormat:       encFormatV1,
		ChecksumAlg:     "sha256",
		ParquetKey:      "billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet",
		ChecksumKey:     "billing/usage_events/year=2026/month=05/usage_events_2026_05.checksum.sha256",
	}
}

func TestRestorePartitionWorkflow_happyPath(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RestorePartitionWorkflow)
	registerRestoreActivityStubs(env)

	manifest := happyManifest()
	env.OnActivity(ActivityNameFetchManifest, mock.Anything, FetchManifestInput{Year: 2026, Month: 5}).
		Return(manifest, nil)
	env.OnActivity(ActivityNameVerifyChecksum, mock.Anything,
		VerifyChecksumInput{ParquetKey: manifest.ParquetKey, ChecksumKey: manifest.ChecksumKey}).
		Return(nil)
	env.OnActivity(ActivityNameDownloadDecrypt, mock.Anything, DownloadDecryptInput{
		Year:            2026,
		Month:           5,
		ParquetKey:      manifest.ParquetKey,
		SourcePartition: manifest.SourcePartition,
		EncFormat:       manifest.EncFormat,
		KEKHint:         manifest.KEKHint,
	}).Return(DownloadDecryptResult{PlaintextPath: "/tmp/p.parquet"}, nil)
	env.OnActivity(ActivityNameLoadQuarantine, mock.Anything,
		LoadQuarantineInput{Year: 2026, Month: 5, PlaintextPath: "/tmp/p.parquet"}).
		Return(LoadQuarantineResult{
			QuarantineTable: "billing.usage_events_restore_2026_05",
			RowsImported:    2,
		}, nil)

	env.ExecuteWorkflow(RestorePartitionWorkflow, RestoreInput{Year: 2026, Month: 5})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result RestoreResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if result.QuarantineTable != "billing.usage_events_restore_2026_05" {
		t.Errorf("QuarantineTable=%q", result.QuarantineTable)
	}
	if result.RowsImported != 2 {
		t.Errorf("RowsImported=%d want 2", result.RowsImported)
	}
	if result.DEKVersionUsed != 3 {
		t.Errorf("DEKVersionUsed=%d want 3", result.DEKVersionUsed)
	}
}

func TestRestorePartitionWorkflow_rejectsUnsupportedEncFormat(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RestorePartitionWorkflow)
	registerRestoreActivityStubs(env)

	manifest := happyManifest()
	manifest.EncFormat = "aes-256-gcm-v2-1mib" // hypothetical future format
	env.OnActivity(ActivityNameFetchManifest, mock.Anything, mock.Anything).Return(manifest, nil)

	env.ExecuteWorkflow(RestorePartitionWorkflow, RestoreInput{Year: 2026, Month: 5})

	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("expected error for unsupported enc_format")
	}
	if !strings.Contains(err.Error(), "unsupported enc_format") {
		t.Errorf("err=%v missing 'unsupported enc_format'", err)
	}
}

func TestRestorePartitionWorkflow_manifestMissingNonRetryable(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RestorePartitionWorkflow)
	registerRestoreActivityStubs(env)

	missing := temporal.NewNonRetryableApplicationError(
		"fetch manifest: object store: not found", "ErrObjectNotFound", errors.New("not found"))
	env.OnActivity(ActivityNameFetchManifest, mock.Anything, mock.Anything).
		Return(FetchManifestResult{}, missing)

	env.ExecuteWorkflow(RestorePartitionWorkflow, RestoreInput{Year: 2026, Month: 5})

	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error, got nil")
	}
}

func TestRestorePartitionWorkflow_checksumMismatchSurfaces(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RestorePartitionWorkflow)
	registerRestoreActivityStubs(env)

	env.OnActivity(ActivityNameFetchManifest, mock.Anything, mock.Anything).Return(happyManifest(), nil)
	env.OnActivity(ActivityNameVerifyChecksum, mock.Anything, mock.Anything).
		Return(temporal.NewNonRetryableApplicationError(
			"verify checksum: parquet checksum mismatch", "ErrChecksumMismatch", ErrChecksumMismatch))

	env.ExecuteWorkflow(RestorePartitionWorkflow, RestoreInput{Year: 2026, Month: 5})

	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error from checksum mismatch")
	}
}

func TestRestorePartitionWorkflow_loadFailureTriggersDropCompensation(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RestorePartitionWorkflow)
	registerRestoreActivityStubs(env)

	env.OnActivity(ActivityNameFetchManifest, mock.Anything, mock.Anything).Return(happyManifest(), nil)
	env.OnActivity(ActivityNameVerifyChecksum, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(ActivityNameDownloadDecrypt, mock.Anything, mock.Anything).
		Return(DownloadDecryptResult{PlaintextPath: "/tmp/p.parquet"}, nil)

	loadErr := temporal.NewNonRetryableApplicationError(
		"load quarantine: copy: pg: out of disk", "PgError", errors.New("disk full"))
	env.OnActivity(ActivityNameLoadQuarantine, mock.Anything, mock.Anything).
		Return(LoadQuarantineResult{}, loadErr)
	dropCalls := 0
	env.OnActivity(ActivityNameDropQuarantine, mock.Anything,
		DropQuarantineInput{Year: 2026, Month: 5}).
		Run(func(args mock.Arguments) { dropCalls++ }).Return(nil)

	env.ExecuteWorkflow(RestorePartitionWorkflow, RestoreInput{Year: 2026, Month: 5})

	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error from load failure")
	}
	if dropCalls != 1 {
		t.Errorf("DropQuarantine calls=%d want 1 (compensation)", dropCalls)
	}
}

func TestRestorePartitionWorkflow_invalidInputRejected(t *testing.T) {
	cases := []struct {
		name string
		in   RestoreInput
	}{
		{"zero", RestoreInput{}},
		{"yearTooLow", RestoreInput{Year: 2025, Month: 5}},
		{"yearTooHigh", RestoreInput{Year: 2100, Month: 5}},
		{"monthZero", RestoreInput{Year: 2026, Month: 0}},
		{"monthThirteen", RestoreInput{Year: 2026, Month: 13}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &testsuite.WorkflowTestSuite{}
			env := s.NewTestWorkflowEnvironment()
			env.RegisterWorkflow(RestorePartitionWorkflow)
			env.ExecuteWorkflow(RestorePartitionWorkflow, tc.in)
			if env.GetWorkflowError() == nil {
				t.Errorf("expected error for invalid input %+v", tc.in)
			}
		})
	}
}

func TestParseKEKVersion(t *testing.T) {
	cases := map[string]int{
		"platform-billing-v1":  1,
		"platform-billing-v42": 42,
		"stub-v0":              0,
		"":                     0,
		"platform-billing-vXX": 0,
	}
	for in, want := range cases {
		if got := parseKEKVersion(in); got != want {
			t.Errorf("parseKEKVersion(%q)=%d want %d", in, got, want)
		}
	}
}

// jsonMarshalManifest is a helper so the activity tests can seed a stub
// object store with a realistic manifest payload.
func jsonMarshalManifest(m archiveManifest) []byte {
	b, _ := json.Marshal(m)
	return b
}
