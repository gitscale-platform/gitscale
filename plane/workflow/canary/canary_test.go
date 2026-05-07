package canary_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/workflow/canary"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// TestCanaryWorkflow_runsActivity exercises the canary workflow inside the
// Temporal SDK's time-skipping test environment. No real Temporal server is
// needed — the WorkflowTestSuite simulates the worker, the history, and
// activity dispatch in-process. This proves the workflow + activity wiring
// and the determinism shape (ExecuteActivity through ActivityOptions +
// DefaultRetryPolicy) without flaking against a docker container.
func TestCanaryWorkflow_runsActivity(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mem := cache.NewMemoryStore(cache.RealClock{})
	if err := mem.Set(context.Background(), canary.HealthKey, []byte("OK"), time.Hour); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	a := canary.NewHealthActivity(mem)
	env.RegisterActivityWithOptions(a.Run, activity.RegisterOptions{Name: canary.HealthActivityName})

	env.ExecuteWorkflow(canary.CanaryWorkflow)

	if !env.IsWorkflowCompleted() {
		t.Fatal("canary workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result string
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result != "OK" {
		t.Errorf("expected workflow result \"OK\", got %q", result)
	}
}

func TestCanaryWorkflow_propagatesCacheMiss(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mem := cache.NewMemoryStore(cache.RealClock{})
	a := canary.NewHealthActivity(mem)
	env.RegisterActivityWithOptions(a.Run, activity.RegisterOptions{Name: canary.HealthActivityName})

	env.ExecuteWorkflow(canary.CanaryWorkflow)

	if !env.IsWorkflowCompleted() {
		t.Fatal("canary workflow did not complete")
	}
	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("expected error from cache miss, got nil")
	}
	if !strings.Contains(err.Error(), canary.ErrCacheMiss.Error()) {
		t.Errorf("expected wrapped ErrCacheMiss in error chain, got: %v", err)
	}
}
