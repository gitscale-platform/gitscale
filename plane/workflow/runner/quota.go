package runner

// validateAgainstQuota returns ErrQuotaInsufficient when shape exceeds the
// per-job ceiling derived from the org's quota envelope. The ceiling is a
// pure function of the monthly compute-minutes cap: a single job may
// consume at most maxJobShareOfMonthlyCap of the monthly budget. Cap
// values <= 0 mean "no entitlement"; the activity rejects.
//
// This function is a pure helper — no I/O, no time. Call sites are the
// boot-cold and lease-hot activities; the workflow body never invokes it
// directly.
func validateAgainstQuota(shape ResourceShape, computeMinutesPerMonthCap int64) error {
	if computeMinutesPerMonthCap <= 0 {
		return ErrQuotaInsufficient
	}
	if shape.VCPU <= 0 || shape.MemoryMB <= 0 || shape.WallClockSeconds <= 0 {
		return ErrInvalidShape
	}
	// Per-job ceiling: at most 1/maxJobShareOfMonthlyCap of the monthly
	// compute-minutes cap. With cap=500 (free tier) and share=5 the
	// per-job ceiling is 100 vcpu-minutes — generous for typical CI work
	// while preventing a single rogue agent from torching a month's
	// budget in one shot.
	const maxJobShareOfMonthlyCap = 5
	perJobCeilingVCPUSeconds := (computeMinutesPerMonthCap * 60) / maxJobShareOfMonthlyCap
	requestedVCPUSeconds := int64(shape.VCPU) * int64(shape.WallClockSeconds)
	if requestedVCPUSeconds > perJobCeilingVCPUSeconds {
		return ErrQuotaInsufficient
	}
	return nil
}
