package workflow

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

// ScheduleClient is the subset of go.temporal.io/sdk/client.ScheduleClient
// EnsureSchedule depends on. *client.Client.ScheduleClient() satisfies it;
// tests inject a fake to assert idempotent behaviour without a live Temporal
// server. Spec D6 (#33).
type ScheduleClient interface {
	Create(ctx context.Context, opts client.ScheduleOptions) (client.ScheduleHandle, error)
	GetHandle(ctx context.Context, id string) client.ScheduleHandle
}

// EnsureSchedule registers a Temporal Schedule, idempotent across worker
// restarts and across concurrent boots. On first call it issues Create; if
// the server reports the schedule already exists, it issues an Update so the
// running spec converges to opts. The intended caller is cmd/workflow-worker
// at boot time and *_test.go fixtures.
//
// opts.ID must be set (Temporal requires a stable ID for Update to find the
// schedule). Returns the live schedule handle in both branches so callers
// can attach further state if needed.
func EnsureSchedule(ctx context.Context, sc ScheduleClient, opts client.ScheduleOptions) (client.ScheduleHandle, error) {
	if opts.ID == "" {
		return nil, errors.New("workflow: EnsureSchedule: opts.ID is required")
	}

	handle, err := sc.Create(ctx, opts)
	if err == nil {
		return handle, nil
	}
	if !errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
		return nil, fmt.Errorf("workflow: EnsureSchedule: create %s: %w", opts.ID, err)
	}

	// Schedule already exists; converge it to opts via Update.
	existing := sc.GetHandle(ctx, opts.ID)
	updateErr := existing.Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			// Carry forward the server-side Policy/State; overwrite Spec + Action
			// so the live schedule converges to opts.
			next := in.Description.Schedule
			next.Action = opts.Action
			specCopy := opts.Spec
			next.Spec = &specCopy
			return &client.ScheduleUpdate{Schedule: &next}, nil
		},
	})
	if updateErr != nil {
		return nil, fmt.Errorf("workflow: EnsureSchedule: update %s: %w", opts.ID, updateErr)
	}
	return existing, nil
}
