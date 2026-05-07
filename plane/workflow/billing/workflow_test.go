package billing

import (
	"context"
	"testing"
	"time"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"go.temporal.io/sdk/testsuite"
)

func TestPartitionRolloverWorkflow_callsActivity_withNextMonth(t *testing.T) {
	cases := []struct {
		name      string
		runTime   time.Time
		wantYear  int
		wantMonth int
	}{
		{"midyear", time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC), 2026, 6},
		{"december", time.Date(2026, 12, 24, 12, 0, 0, 0, time.UTC), 2027, 1},
		{"calendarBomb", time.Date(2027, 4, 24, 12, 0, 0, 0, time.UTC), 2027, 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &testsuite.WorkflowTestSuite{}
			env := s.NewTestWorkflowEnvironment()

			stub := billingstore.NewStubPartitioner()
			act, err := NewCreatePartitionActivity(stub)
			if err != nil {
				t.Fatal(err)
			}
			env.RegisterWorkflow(PartitionRolloverWorkflow)
			env.RegisterActivityWithOptions(act.Execute, registerActivityOpts())

			env.ExecuteWorkflow(PartitionRolloverWorkflow, PartitionRolloverInput{RunTime: tc.runTime})
			if !env.IsWorkflowCompleted() {
				t.Fatal("workflow did not complete")
			}
			if err := env.GetWorkflowError(); err != nil {
				t.Fatalf("workflow error: %v", err)
			}
			var result CreatePartitionResult
			if err := env.GetWorkflowResult(&result); err != nil {
				t.Fatalf("GetWorkflowResult: %v", err)
			}
			wantPartition := partitionName(tc.wantYear, tc.wantMonth)
			if result.PartitionName != wantPartition {
				t.Errorf("PartitionName=%s want %s", result.PartitionName, wantPartition)
			}
			if !result.Created {
				t.Errorf("Created=false on first run")
			}
			calls := stub.Calls()
			if calls[partitionLeaf(tc.wantYear, tc.wantMonth)] != 1 {
				t.Errorf("activity calls=%v want one for %d-%02d", calls, tc.wantYear, tc.wantMonth)
			}
		})
	}
}

func TestPartitionRolloverWorkflow_idempotentReplay(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}

	stub := billingstore.NewStubPartitioner()
	act, _ := NewCreatePartitionActivity(stub)

	// Run the workflow twice with the same input. Both runs should succeed;
	// the second should report Created=false.
	for i, wantCreated := range []bool{true, false} {
		env := s.NewTestWorkflowEnvironment()
		env.RegisterWorkflow(PartitionRolloverWorkflow)
		env.RegisterActivityWithOptions(act.Execute, registerActivityOpts())

		env.ExecuteWorkflow(PartitionRolloverWorkflow,
			PartitionRolloverInput{RunTime: time.Date(2027, 4, 24, 12, 0, 0, 0, time.UTC)},
		)
		if err := env.GetWorkflowError(); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		var r CreatePartitionResult
		_ = env.GetWorkflowResult(&r)
		if r.Created != wantCreated {
			t.Errorf("run %d: Created=%v want %v", i, r.Created, wantCreated)
		}
	}
}

func TestCreatePartitionActivity_directInvocation(t *testing.T) {
	stub := billingstore.NewStubPartitioner()
	act, err := NewCreatePartitionActivity(stub)
	if err != nil {
		t.Fatal(err)
	}
	got, err := act.Execute(context.Background(), CreatePartitionInput{Year: 2027, Month: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Created {
		t.Error("expected Created=true on first call")
	}
	got, err = act.Execute(context.Background(), CreatePartitionInput{Year: 2027, Month: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got.Created {
		t.Error("expected Created=false on second call")
	}
}

func TestNewCreatePartitionActivity_nilPartitioner(t *testing.T) {
	if _, err := NewCreatePartitionActivity(nil); err == nil {
		t.Error("expected error from nil partitioner")
	}
}

func TestNextMonthFrom(t *testing.T) {
	cases := []struct {
		in   time.Time
		want [2]int
	}{
		{time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), [2]int{2026, 2}},
		{time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), [2]int{2027, 1}},
		{time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC), [2]int{2024, 3}},
	}
	for _, tc := range cases {
		y, m := nextMonthFrom(tc.in)
		if y != tc.want[0] || m != tc.want[1] {
			t.Errorf("nextMonthFrom(%v)=%d-%02d want %d-%02d", tc.in, y, m, tc.want[0], tc.want[1])
		}
	}
}

// helpers --------------------------------------------------------------------

func partitionName(year, month int) string {
	return "billing." + partitionLeaf(year, month)
}

func partitionLeaf(year, month int) string {
	return "usage_events_" + zeroPad4(year) + "_" + zeroPad2(month)
}

func zeroPad4(n int) string {
	s := []byte("0000")
	v := n
	for i := 3; i >= 0; i-- {
		s[i] = byte('0' + v%10)
		v /= 10
	}
	return string(s)
}

func zeroPad2(n int) string {
	s := []byte("00")
	v := n
	for i := 1; i >= 0; i-- {
		s[i] = byte('0' + v%10)
		v /= 10
	}
	return string(s)
}
