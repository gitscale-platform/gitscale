package ci

import (
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/gitscale-platform/gitscale/plane/workflow/runner"
)

// CIJobWorkflow is the single workflow per CI job (#110). Deterministic
// per ADR-003: assignTier is pure; all I/O is in activities; teardown
// runs through workflow.NewDisconnectedContext so cancellation is safe.
//
// Failure semantics (spec §"Failure modes"):
//   - boot fails before VM allocated:   error returned, no teardown
//   - boot succeeds, run fails:         teardown runs, emission still happens
//   - run succeeds, emission fails:     activity retries; permanent fail surfaces
//   - cancellation mid-run:             disconnected teardown + emission
//
// The function MUST NOT use clock reads, randomness sources, env reads,
// network calls, goroutines, channels or sync primitives. The
// plane/workflow/lint scanner enforces this contract.
func CIJobWorkflow(ctx workflow.Context, in CIJobInput) (CIJobOutput, error) {
	if !in.PrincipalKind.IsValid() {
		return CIJobOutput{}, temporal.NewNonRetryableApplicationError(
			"ci: invalid PrincipalKind on workflow input", "InvalidInput", nil)
	}
	tier := assignTier(in.PrincipalKind, in.Annotations)

	// Boot phase: pick activity by tier; explicit per-tier ActivityOptions
	// per spec §"Activity timeouts". No DefaultActivityOptions anywhere.
	var handle runner.MicroVMHandle
	bootCtx := workflow.WithActivityOptions(ctx, bootOptionsFor(tier))
	switch tier {
	case TierCold:
		err := workflow.ExecuteActivity(bootCtx, runner.ActivityNameBootColdVM, runner.BootInput{
			JobID:       in.JobID,
			PrincipalID: in.PrincipalID,
			OrgID:       in.OrgID,
			Resource:    in.Resource,
		}).Get(ctx, &handle)
		if err != nil {
			return CIJobOutput{Tier: tier}, err
		}
	case TierHot:
		err := workflow.ExecuteActivity(bootCtx, runner.ActivityNameLeaseHotVM, runner.LeaseInput{
			JobID:       in.JobID,
			PrincipalID: in.PrincipalID,
			OrgID:       in.OrgID,
			Resource:    in.Resource,
		}).Get(ctx, &handle)
		if err != nil {
			return CIJobOutput{Tier: tier}, err
		}
	default:
		return CIJobOutput{Tier: tier}, temporal.NewNonRetryableApplicationError(
			"ci: assignTier produced TierUnknown", "InvalidTier", nil)
	}

	// Run phase. Single attempt — CI jobs are not idempotent. Pre-compute
	// the env-key sort here so the activity sees a deterministic order
	// even if the worker rehydrates the map across replays.
	runCtx := workflow.WithActivityOptions(ctx, runOptions(in.Resource.WallClockSeconds))
	var result runner.JobResult
	runErr := workflow.ExecuteActivity(runCtx, runner.ActivityNameRunJob, runner.RunInput{
		VMID:    handle.ID,
		Command: in.Command,
		Env:     in.Env,
		EnvKeys: sortedKeys(in.Env),
	}).Get(ctx, &result)
	if runErr != nil && result.ExitCode == 0 {
		// Activity surfaced a transport / VM-loss failure rather than a
		// command exit. Stamp ExitCode = -1 so the billing emission
		// reflects "ran but failed" vs the cancellation case (-2).
		result.ExitCode = -1
	}

	// Teardown via disconnected context — survives workflow cancellation.
	// Errors here are logged by the activity and bubbled, but emission
	// still runs below so consumed compute is always charged.
	disconnectedCtx, _ := workflow.NewDisconnectedContext(ctx)
	teardownCtx := workflow.WithActivityOptions(disconnectedCtx, teardownOptions())
	_ = workflow.ExecuteActivity(teardownCtx, runner.ActivityNameTeardownVM, handle.ID).Get(disconnectedCtx, nil)

	// Emission phase. Always runs — successful and failed jobs both
	// consume vcpu-seconds and must hit the billing outbox. Cancelled
	// workflows reach this branch only via the disconnected context.
	if errors.Is(ctx.Err(), workflow.ErrCanceled) {
		// Cancellation marker per spec §"Failure modes".
		result.ExitCode = -2
	}
	emitCtx := workflow.WithActivityOptions(disconnectedCtx, emitOptions())
	emitErr := workflow.ExecuteActivity(emitCtx, runner.ActivityNameEmitUsageEvent, runner.UsageInput{
		JobID:         in.JobID,
		PrincipalID:   in.PrincipalID,
		PrincipalKind: in.PrincipalKind.String(),
		OrgID:         in.OrgID,
		RepoID:        in.RepoID,
		Tier:          tier.String(),
		Result:        result,
	}).Get(disconnectedCtx, nil)

	switch {
	case runErr != nil:
		// Run failure dominates. Emission failure is logged but the
		// workflow result reflects the run outcome.
		return CIJobOutput{Tier: tier, VMID: handle.ID, Result: result, ExitCode: result.ExitCode}, runErr
	case emitErr != nil:
		return CIJobOutput{Tier: tier, VMID: handle.ID, Result: result, ExitCode: result.ExitCode}, emitErr
	default:
		return CIJobOutput{Tier: tier, VMID: handle.ID, Result: result, ExitCode: result.ExitCode}, nil
	}
}

// bootOptionsFor returns explicit per-tier ActivityOptions. Cold path
// gets 60 s and 3 attempts; hot path gets 5 s and 5 attempts. Both
// classify ErrQuotaInsufficient and ErrInvalidShape as non-retryable.
func bootOptionsFor(tier Tier) workflow.ActivityOptions {
	switch tier {
	case TierHot:
		return workflow.ActivityOptions{
			TaskQueue:           "", // inherit
			StartToCloseTimeout: 5 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    200 * time.Millisecond,
				BackoffCoefficient: 1.5,
				MaximumAttempts:    5,
				NonRetryableErrorTypes: []string{
					nonRetryableQuotaErrorType,
					nonRetryableShapeErrorType,
				},
			},
		}
	default: // TierCold
		return workflow.ActivityOptions{
			StartToCloseTimeout: 60 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    1 * time.Second,
				BackoffCoefficient: 2.0,
				MaximumAttempts:    3,
				NonRetryableErrorTypes: []string{
					nonRetryableQuotaErrorType,
					nonRetryableShapeErrorType,
				},
			},
		}
	}
}

// runOptions returns the ActivityOptions for RunJobActivity. Single
// attempt — CI jobs are NOT idempotent. Timeout is WallClock + 60 s
// grace.
func runOptions(wallClockSeconds int) workflow.ActivityOptions {
	if wallClockSeconds <= 0 {
		wallClockSeconds = runner.DefaultResourceShape.WallClockSeconds
	}
	return workflow.ActivityOptions{
		StartToCloseTimeout: time.Duration(wallClockSeconds+60) * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
}

// teardownOptions returns the ActivityOptions for TeardownVMActivity.
// Idempotent — safe to retry.
func teardownOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    5,
		},
	}
}

// emitOptions returns the ActivityOptions for EmitUsageEventActivity.
// Generous retries — failure here means a missed billing event.
func emitOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    10,
		},
	}
}

// nonRetryableQuotaErrorType / nonRetryableShapeErrorType are the
// stringified error type names that boot activities use when wrapping
// runner.ErrQuotaInsufficient / runner.ErrInvalidShape via
// temporal.NewNonRetryableApplicationError. The activities propagate
// these names so the workflow's RetryPolicy can suppress retries.
const (
	nonRetryableQuotaErrorType = "QuotaInsufficient"
	nonRetryableShapeErrorType = "InvalidShape"
)
