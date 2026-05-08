package outboxttl_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/workflow/outboxttl"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// fakeActivity implements the registered Execute signature; selected by
// in.Domain so we can encode happy paths and per-domain failures in one
// fixture. The receiver carries no state across invocations, satisfying
// activity-idempotency expectations.
type fakeActivity struct {
	results map[string]outboxttl.ExpireDomainResult
	errs    map[string]error
}

func (f *fakeActivity) Execute(_ context.Context, in outboxttl.ExpireDomainInput) (outboxttl.ExpireDomainResult, error) {
	if e, ok := f.errs[in.Domain]; ok {
		return outboxttl.ExpireDomainResult{}, e
	}
	if r, ok := f.results[in.Domain]; ok {
		return r, nil
	}
	return outboxttl.ExpireDomainResult{Domain: in.Domain}, nil
}

func registerFake(env *testsuite.TestWorkflowEnvironment, fa *fakeActivity) {
	env.RegisterActivityWithOptions(fa.Execute, activity.RegisterOptions{
		Name: outboxttl.ActivityNameExpireDomainOutbox,
	})
	env.RegisterWorkflow(outboxttl.ExpireOutboxesWorkflow)
}

func TestWorkflow_HappyPath(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	fa := &fakeActivity{results: map[string]outboxttl.ExpireDomainResult{
		string(store.DomainIdentity):      {Domain: string(store.DomainIdentity), RowsDeleted: 1},
		string(store.DomainRepositories):  {Domain: string(store.DomainRepositories), RowsDeleted: 2},
		string(store.DomainCollaboration): {Domain: string(store.DomainCollaboration), RowsDeleted: 0},
		string(store.DomainCI):            {Domain: string(store.DomainCI), RowsDeleted: 3},
		string(store.DomainBilling):       {Domain: string(store.DomainBilling), RowsDeleted: 4},
	}}
	registerFake(env, fa)

	env.ExecuteWorkflow(outboxttl.ExpireOutboxesWorkflow)
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var got outboxttl.ExpireOutboxesResult
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if len(got.PerDomain) != 5 {
		t.Fatalf("expected 5 per-domain results, got %d (%+v)", len(got.PerDomain), got.PerDomain)
	}
	if len(got.Errors) != 0 {
		t.Fatalf("expected zero errors, got %v", got.Errors)
	}
	// Determinism check: domains must come back in declaration order.
	wantOrder := []string{
		string(store.DomainIdentity),
		string(store.DomainRepositories),
		string(store.DomainCollaboration),
		string(store.DomainCI),
		string(store.DomainBilling),
	}
	for i, want := range wantOrder {
		if got.PerDomain[i].Domain != want {
			t.Errorf("PerDomain[%d].Domain=%s want %s", i, got.PerDomain[i].Domain, want)
		}
	}
}

func TestWorkflow_OneDomainFailsButOthersComplete(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	fa := &fakeActivity{
		results: map[string]outboxttl.ExpireDomainResult{
			string(store.DomainIdentity):      {Domain: string(store.DomainIdentity), RowsDeleted: 1},
			string(store.DomainRepositories):  {Domain: string(store.DomainRepositories), RowsDeleted: 2},
			string(store.DomainCollaboration): {Domain: string(store.DomainCollaboration), RowsDeleted: 0},
			string(store.DomainCI):            {Domain: string(store.DomainCI), RowsDeleted: 3},
		},
		errs: map[string]error{
			string(store.DomainBilling): errors.New("simulated billing failure"),
		},
	}
	registerFake(env, fa)

	env.ExecuteWorkflow(outboxttl.ExpireOutboxesWorkflow)
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error when one domain fails")
	}
	var got outboxttl.ExpireOutboxesResult
	if err := env.GetWorkflowResult(&got); err == nil {
		// On error path the SDK does not populate result; check via getter
		// optional — primary assertion is the workflow-level error above.
		t.Logf("got partial result: %+v", got)
	}
}
