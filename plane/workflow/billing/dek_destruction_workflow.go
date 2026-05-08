package billing

import (
	"fmt"
	"time"

	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/workflow"
)

// DEKDestructionWorkflowName is the registered name for DEKDestructionWorkflow.
const DEKDestructionWorkflowName = "billing.DEKDestructionWorkflow"

// DEKDestructionRetentionDays is the retention floor before a partition's
// per-month DEK becomes eligible for crypto-shred. ADR-018 §Encryption
// fixes this at 7 years + 30 days; the +30d gives a grace window for
// late-arriving legal holds.
//
// The router workflow subtracts this duration from workflow.Now to compute
// the cutoff at fire time, preserving determinism.
const DEKDestructionRetentionDays = 365*7 + 30

// DEKDestructionInput is the input to DEKDestructionWorkflow.
//
// RunTime is bound at fire time by the schedule (via the router workflow);
// it is propagated through to activity inputs that capture timestamps.
// Cutoff is the precomputed (RunTime - 7y30d) the router passes through —
// computing it once in the router keeps the inner workflow deterministic
// across replays even if the constant is bumped between deploys.
type DEKDestructionInput struct {
	RunTime time.Time
	Cutoff  time.Time
}

// DEKDestructionResult summarises a workflow run.
type DEKDestructionResult struct {
	PartitionsScanned int
	KeysDestroyed     int
	// Skipped is one entry per partition that was scanned but not
	// destroyed; the value is "<year>-<month>-<partition_name>: <reason>".
	// Reasons are: "missing_kek_hint", "legal_hold:<hold_reason>",
	// "approval_rejected:<reason>", "destroy_error:<error>",
	// "emit_error:<error>".
	Skipped []string
}

// DEKDestructionWorkflow scans billing.partition_archives for partitions
// older than (RunTime - 7y30d) and crypto-shreds their per-month Vault
// transit key versions (ADR-018 §Encryption). Pre-flight legal-hold check
// and ADR-015 operator-approval gate run before the irreversible Vault
// trim. Per-partition errors do not abort the run; they are accumulated
// in Result.Skipped.
//
// Determinism: workflow.Now is not called here — Cutoff is precomputed by
// the router and embedded in the input. Iteration order is deterministic
// because ListEligiblePartitionsActivity returns a sorted slice.
func DEKDestructionWorkflow(ctx workflow.Context, in DEKDestructionInput) (DEKDestructionResult, error) {
	if in.Cutoff.IsZero() {
		return DEKDestructionResult{}, fmt.Errorf("dek destruction: cutoff is zero")
	}

	listOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	checkOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	// Approval may legitimately block on a human; long timeout + heartbeat-
	// friendly retry policy. Tests inject a synchronous mock and never see
	// this timeout.
	approvalOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 24 * time.Hour,
		HeartbeatTimeout:    5 * time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	destroyOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	emitOpts := workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}

	listCtx := workflow.WithActivityOptions(ctx, listOpts)
	var listResult ListEligibleResult
	if err := workflow.ExecuteActivity(listCtx, ActivityNameListEligiblePartitions,
		ListEligibleInput{Cutoff: in.Cutoff},
	).Get(listCtx, &listResult); err != nil {
		return DEKDestructionResult{}, fmt.Errorf("dek destruction: list: %w", err)
	}

	result := DEKDestructionResult{
		PartitionsScanned: len(listResult.Partitions),
	}

	for _, p := range listResult.Partitions {
		tag := fmt.Sprintf("%04d-%02d-%s", p.Year, p.Month, p.PartitionName)

		if p.KEKHint == "" {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: missing_kek_hint", tag))
			continue
		}

		// 1. Legal-hold gate.
		checkCtx := workflow.WithActivityOptions(ctx, checkOpts)
		var hold LegalHoldCheckResult
		if err := workflow.ExecuteActivity(checkCtx, ActivityNameCheckLegalHold,
			LegalHoldCheckInput{
				Year:          p.Year,
				Month:         p.Month,
				PartitionName: p.PartitionName,
				LakeURI:       p.LakeURI,
			},
		).Get(checkCtx, &hold); err != nil {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: legal_hold_error:%v", tag, err))
			continue
		}
		if hold.Held {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: legal_hold:%s", tag, hold.Reason))
			continue
		}

		// 2. Operator-approval gate (ADR-015).
		approvalCtx := workflow.WithActivityOptions(ctx, approvalOpts)
		var approval OperatorApprovalResult
		if err := workflow.ExecuteActivity(approvalCtx, ActivityNameRequestOperatorApproval,
			OperatorApprovalInput{
				Year:          p.Year,
				Month:         p.Month,
				PartitionName: p.PartitionName,
				KEKHint:       p.KEKHint,
			},
		).Get(approvalCtx, &approval); err != nil {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: approval_error:%v", tag, err))
			continue
		}
		if !approval.Approved {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: approval_rejected:%s", tag, approval.Reason))
			continue
		}

		// 3. Destroy DEK (irreversible).
		destroyCtx := workflow.WithActivityOptions(ctx, destroyOpts)
		var destroy DestroyDEKResult
		if err := workflow.ExecuteActivity(destroyCtx, ActivityNameDestroyDEK,
			DestroyDEKInput{Year: p.Year, Month: p.Month, KEKHint: p.KEKHint},
		).Get(destroyCtx, &destroy); err != nil {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: destroy_error:%v", tag, err))
			continue
		}

		// 4. Emit audit event. A failure after the destroy succeeded
		// surfaces as a skip, NOT a workflow abort: the irreversible side
		// effect already happened, so we record the gap in result rather
		// than letting Temporal restart and double-emit on a later attempt.
		// Operator runbook covers the manual-replay path.
		emitCtx := workflow.WithActivityOptions(ctx, emitOpts)
		if err := workflow.ExecuteActivity(emitCtx, ActivityNameEmitDEKDestroyed,
			EmitDEKDestroyedInput{
				Year:            p.Year,
				Month:           p.Month,
				PartitionName:   p.PartitionName,
				KEKHint:         p.KEKHint,
				VaultKeyVersion: destroy.VaultKeyVersion,
			},
		).Get(emitCtx, nil); err != nil {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: emit_error:%v", tag, err))
			// Still count the destruction — the audit gap is the operator's
			// problem, not a reason to under-report what was crypto-shredded.
			result.KeysDestroyed++
			continue
		}

		result.KeysDestroyed++
	}

	return result, nil
}
