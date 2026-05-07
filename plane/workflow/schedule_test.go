package workflow

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

type fakeSchedule struct {
	id            string
	updateCalls   int
	lastUpdateIn  client.ScheduleUpdateInput
	lastUpdateOut *client.ScheduleUpdate
	updateErr     error
}

func (f *fakeSchedule) GetID() string                                                    { return f.id }
func (f *fakeSchedule) Delete(_ context.Context) error                                   { return nil }
func (f *fakeSchedule) Backfill(_ context.Context, _ client.ScheduleBackfillOptions) error { return nil }
func (f *fakeSchedule) Update(_ context.Context, opts client.ScheduleUpdateOptions) error {
	f.updateCalls++
	current := client.ScheduleUpdateInput{
		Description: client.ScheduleDescription{
			Schedule: client.Schedule{},
		},
	}
	f.lastUpdateIn = current
	out, err := opts.DoUpdate(current)
	if err != nil {
		return err
	}
	f.lastUpdateOut = out
	return f.updateErr
}
func (f *fakeSchedule) Describe(_ context.Context) (*client.ScheduleDescription, error) {
	return &client.ScheduleDescription{}, nil
}
func (f *fakeSchedule) Trigger(_ context.Context, _ client.ScheduleTriggerOptions) error  { return nil }
func (f *fakeSchedule) Pause(_ context.Context, _ client.SchedulePauseOptions) error      { return nil }
func (f *fakeSchedule) Unpause(_ context.Context, _ client.ScheduleUnpauseOptions) error  { return nil }

type fakeScheduleClient struct {
	createCalls int
	createErr   error
	handle      *fakeSchedule
}

func (f *fakeScheduleClient) Create(_ context.Context, opts client.ScheduleOptions) (client.ScheduleHandle, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.handle = &fakeSchedule{id: opts.ID}
	return f.handle, nil
}

func (f *fakeScheduleClient) GetHandle(_ context.Context, id string) client.ScheduleHandle {
	if f.handle == nil {
		f.handle = &fakeSchedule{id: id}
	}
	return f.handle
}

func newOpts() client.ScheduleOptions {
	return client.ScheduleOptions{
		ID: "billing-partition-rollover",
		Spec: client.ScheduleSpec{
			CronExpressions: []string{"0 12 24 * *"},
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        "billing-partition-rollover-test",
			Workflow:  "PartitionRolloverWorkflow",
			TaskQueue: QueueBillingMaintenance,
		},
	}
}

func TestEnsureSchedule_createPath(t *testing.T) {
	sc := &fakeScheduleClient{}
	h, err := EnsureSchedule(context.Background(), sc, newOpts())
	if err != nil {
		t.Fatalf("EnsureSchedule: %v", err)
	}
	if sc.createCalls != 1 {
		t.Errorf("createCalls=%d want 1", sc.createCalls)
	}
	if h == nil {
		t.Error("nil handle")
	}
	if sc.handle != nil && sc.handle.updateCalls != 0 {
		t.Errorf("update should not be called on create path; updateCalls=%d", sc.handle.updateCalls)
	}
}

func TestEnsureSchedule_alreadyExists_updates(t *testing.T) {
	sc := &fakeScheduleClient{createErr: temporal.ErrScheduleAlreadyRunning}
	h, err := EnsureSchedule(context.Background(), sc, newOpts())
	if err != nil {
		t.Fatalf("EnsureSchedule: %v", err)
	}
	if h == nil {
		t.Error("nil handle")
	}
	if sc.handle.updateCalls != 1 {
		t.Errorf("updateCalls=%d want 1", sc.handle.updateCalls)
	}
	if sc.handle.lastUpdateOut == nil || sc.handle.lastUpdateOut.Schedule == nil {
		t.Fatal("update body returned no schedule")
	}
	got := sc.handle.lastUpdateOut.Schedule
	if got.Spec == nil || len(got.Spec.CronExpressions) != 1 || got.Spec.CronExpressions[0] != "0 12 24 * *" {
		t.Errorf("update did not propagate Spec.CronExpressions: %+v", got.Spec)
	}
	if got.Action == nil {
		t.Error("update did not propagate Action")
	}
}

func TestEnsureSchedule_emptyID(t *testing.T) {
	sc := &fakeScheduleClient{}
	if _, err := EnsureSchedule(context.Background(), sc, client.ScheduleOptions{}); err == nil {
		t.Error("expected error for empty ID")
	}
	if sc.createCalls != 0 {
		t.Errorf("Create should not be invoked with empty ID; createCalls=%d", sc.createCalls)
	}
}

func TestEnsureSchedule_nonRetryableCreateError(t *testing.T) {
	sentinel := errors.New("network error")
	sc := &fakeScheduleClient{createErr: sentinel}
	if _, err := EnsureSchedule(context.Background(), sc, newOpts()); !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel propagated, got %v", err)
	}
}

