package cirunclient_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/mcp/cirunclient"
	"github.com/google/uuid"
)

func TestStubClient_RecordsAndReturnsHandle(t *testing.T) {
	stub := &cirunclient.StubClient{
		Handle: cirunclient.RunHandle{WorkflowID: "wf-1", RunID: "run-1"},
	}
	in := cirunclient.CIRunInput{RepoID: uuid.New(), Ref: "refs/heads/main", PrincipalID: uuid.New()}
	got, err := stub.StartCIRun(context.Background(), in)
	if err != nil {
		t.Fatalf("StartCIRun: %v", err)
	}
	if got.WorkflowID != "wf-1" || got.RunID != "run-1" {
		t.Errorf("handle: got %+v", got)
	}
	if !stub.Called || stub.Last != in {
		t.Errorf("recorded: %+v / %+v", stub.Called, stub.Last)
	}
}

func TestNilClient_ReturnsNotConfigured(t *testing.T) {
	_, err := cirunclient.NilClient{}.StartCIRun(context.Background(), cirunclient.CIRunInput{})
	if !errors.Is(err, cirunclient.ErrNotConfigured) {
		t.Errorf("err: got %v want ErrNotConfigured", err)
	}
}

func TestNewTemporalClient_ValidatesArgs(t *testing.T) {
	if _, err := cirunclient.NewTemporalClient(nil, "q"); err == nil {
		t.Error("expected error on nil client")
	}
}
