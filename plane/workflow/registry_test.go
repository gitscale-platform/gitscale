package workflow

import "testing"

// fakeRegistrar records the order in which workflows and activities are
// registered so we can assert Bundle.Apply behaviour without depending on
// the Temporal SDK.
type fakeRegistrar struct {
	workflows  []any
	activities []any
}

func (f *fakeRegistrar) RegisterWorkflow(w any)  { f.workflows = append(f.workflows, w) }
func (f *fakeRegistrar) RegisterActivity(a any)  { f.activities = append(f.activities, a) }

func TestBundle_Apply_registersInOrder(t *testing.T) {
	wf1 := func() {}
	wf2 := func() {}
	a1 := func() {}
	a2 := func() {}

	b := Bundle{
		TaskQueue:  QueueBillingMaintenance,
		Workflows:  []any{wf1, wf2},
		Activities: []any{a1, a2},
	}

	r := &fakeRegistrar{}
	b.Apply(r)

	if len(r.workflows) != 2 || len(r.activities) != 2 {
		t.Fatalf("counts: %d workflows, %d activities", len(r.workflows), len(r.activities))
	}
}

func TestBundle_Apply_emptyBundle_noRegistration(t *testing.T) {
	b := Bundle{TaskQueue: QueueBillingMaintenance}
	r := &fakeRegistrar{}
	b.Apply(r)

	if len(r.workflows) != 0 || len(r.activities) != 0 {
		t.Fatalf("empty bundle: %d workflows, %d activities", len(r.workflows), len(r.activities))
	}
}

func TestAllQueues_includesAllConstants(t *testing.T) {
	want := map[string]bool{
		QueueBillingMaintenance: false,
		QueueAgentSessions:      false,
		QueueCIPipelines:        false,
	}
	for _, q := range AllQueues {
		if _, ok := want[q]; !ok {
			t.Errorf("AllQueues contains unknown queue %q", q)
		}
		want[q] = true
	}
	for q, seen := range want {
		if !seen {
			t.Errorf("AllQueues is missing %q", q)
		}
	}
}
