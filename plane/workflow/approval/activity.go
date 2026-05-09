package approval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/gitscale-platform/gitscale/plane/application/policy"
	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

// ActivityNameRequestApproval is the registered activity name. Stable so
// workflow tests can dispatch by string without holding the activity
// reference.
const ActivityNameRequestApproval = "approval.RequestApproval"

// PollInterval is how often the activity polls the application plane while
// the plan is pending. Selected to balance load on the policy service
// against decision latency. Production tuning is acceptable; the workflow
// re-records the activity invocation across replays so changing this in
// one deploy does not violate determinism.
const PollInterval = 2 * time.Second

// SLAJitter is added to each rung's SLA before the activity calls Escalate.
// Prevents thundering-herd against the policy service when many activities
// time out simultaneously.
const SLAJitter = 5 * time.Second

// Input is the activity parameter. The activity stamps the plan ID before
// returning the result; callers receive the assigned ID via Result.PlanID.
type Input struct {
	OrgID        uuid.UUID
	RepoID       *uuid.UUID
	ProposerID   uuid.UUID
	ProposerKind policy.ProposerKind
	Actions      []policy.Action
}

// Result reports the final disposition of the approval cycle.
type Result struct {
	PlanID uuid.UUID
	Status policy.PlanStatus
}

// Activity wraps a PolicyClient. Its single Execute method submits the
// plan and long-polls the application plane until the plan is no longer
// pending, escalating when the current rung's SLA elapses.
type Activity struct {
	client       appclient.PolicyClient
	pollInterval time.Duration
	slaJitter    time.Duration
	now          func() time.Time
}

// NewActivity wraps client with default poll cadence.
func NewActivity(client appclient.PolicyClient) *Activity {
	return &Activity{
		client:       client,
		pollInterval: PollInterval,
		slaJitter:    SLAJitter,
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// SetTimingForTest overrides the poll interval, SLA jitter, and clock.
// Used only by activity tests; production must not call this.
func (a *Activity) SetTimingForTest(pollInterval, slaJitter time.Duration, now func() time.Time) {
	a.pollInterval = pollInterval
	a.slaJitter = slaJitter
	if now != nil {
		a.now = now
	}
}

// Execute submits the plan via the application plane, then polls for the
// decision. On SLA breach it calls Escalate; on a non-pending status it
// returns. The activity returns an error only on transport faults — a
// rejected/auto_denied plan is a successful execution that surfaces the
// status to the workflow caller.
func (a *Activity) Execute(ctx context.Context, in Input) (Result, error) {
	if a.client == nil {
		return Result{}, errors.New("approval: nil PolicyClient")
	}
	if !in.ProposerKind.IsValid() {
		return Result{}, fmt.Errorf("approval: invalid proposer_kind %q", in.ProposerKind)
	}
	plan := &policy.Plan{
		OrgID:        in.OrgID,
		RepoID:       in.RepoID,
		ProposerID:   in.ProposerID,
		ProposerKind: in.ProposerKind,
		Actions:      in.Actions,
	}
	d, err := a.client.SubmitPlan(ctx, plan)
	if err != nil {
		return Result{}, fmt.Errorf("approval: submit: %w", err)
	}
	// Auto-approve-no-rule and auto-denied are terminal at submission.
	if d.AutoApproved || d.Status == policy.PlanStatusAutoApprovedNoRule {
		return Result{PlanID: d.PlanID, Status: d.Status}, nil
	}
	if d.Status != policy.PlanStatusPending {
		return Result{PlanID: d.PlanID, Status: d.Status}, nil
	}

	// Long-poll loop: poll every pollInterval; when the rung's SLA elapses,
	// call Escalate. Loop exits when status leaves pending or context is
	// canceled.
	currentRung := 0
	rungEnteredAt := a.now()
	for {
		// Honour cancellation; ctx.Err() takes precedence over poll errors.
		if err := ctx.Err(); err != nil {
			return Result{PlanID: d.PlanID, Status: policy.PlanStatusPending}, err
		}

		status, err := a.client.GetPlanStatus(ctx, d.PlanID)
		if err != nil {
			return Result{}, fmt.Errorf("approval: poll: %w", err)
		}
		if status != policy.PlanStatusPending {
			return Result{PlanID: d.PlanID, Status: status}, nil
		}

		// SLA check uses the rung this activity entered into. The decision
		// payload carries the full ladder; we pick the current rung from it.
		if currentRung >= len(d.Ladder) {
			// Defensive: ladder exhausted shouldn't happen in steady state
			// because Escalate finalises with auto_denied. Surface as error.
			return Result{}, errors.New("approval: ladder exhausted without decision")
		}
		rung := d.Ladder[currentRung]
		deadline := rungEnteredAt.Add(time.Duration(rung.SLASeconds)*time.Second + a.slaJitter)
		if a.now().After(deadline) {
			out, err := a.client.Escalate(ctx, policy.EscalateInput{
				PlanID:   d.PlanID,
				FromRung: currentRung,
				Reason:   "sla_breach",
			})
			if err != nil {
				return Result{}, fmt.Errorf("approval: escalate: %w", err)
			}
			if out.FinalRung {
				// Escalate already moved the plan to a terminal state; the
				// next poll will see it and return.
				continue
			}
			currentRung = out.NewRung
			rungEnteredAt = a.now()
			continue
		}

		// Sleep before next poll; honour cancellation during sleep.
		select {
		case <-ctx.Done():
			return Result{PlanID: d.PlanID, Status: policy.PlanStatusPending}, ctx.Err()
		case <-time.After(a.pollInterval):
		}
	}
}
