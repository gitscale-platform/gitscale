package issuenoise

import "time"

// Verdict is the routing decision produced by Decide. Verdict values
// are stable strings on the wire (see VerdictString) so the outbox
// payload survives JSON round-trip without coupling consumers to the
// integer ordinal.
type Verdict int

const (
	// VerdictUnknown is the zero value; never returned by Decide on a
	// well-formed Score. Callers should treat it as a programmer error.
	VerdictUnknown Verdict = iota
	// VerdictNormal admits the issue to the standard queue.
	VerdictNormal
	// VerdictLowQuality holds the issue in the maintainer queue.
	VerdictLowQuality
	// VerdictDuplicate holds the issue and links it to a candidate parent.
	VerdictDuplicate
	// VerdictSpam drops the issue (auto-close with canned reason).
	VerdictSpam
)

// String returns the on-wire string representation of v. Stable across
// releases — changing these constants is a breaking outbox-event change.
func (v Verdict) String() string {
	switch v {
	case VerdictNormal:
		return "normal"
	case VerdictLowQuality:
		return "low_quality"
	case VerdictDuplicate:
		return "duplicate"
	case VerdictSpam:
		return "spam"
	default:
		return "unknown"
	}
}

// IsTerminal reports whether v is a hold-or-drop verdict. Used by the
// router to decide whether to start an IssueHoldExpiryWorkflow (held
// verdicts) or auto-close immediately (spam) or admit (normal).
func (v Verdict) IsTerminal() bool {
	return v == VerdictSpam
}

// IsHeld reports whether v keeps the issue in the maintainer queue.
func (v Verdict) IsHeld() bool {
	return v == VerdictLowQuality || v == VerdictDuplicate
}

// Thresholds are per-repo policy. Defaults selected by the supervisor
// per the spec; per-repo overrides come from issue_noise_config.
type Thresholds struct {
	SpamFloor       float64       // default 0.7
	LowQualityFloor float64       // default 0.4
	DuplicateFloor  float64       // default 0.85
	HoldTTL         time.Duration // default 14 * 24h
}

// DefaultThresholds returns the platform-wide default thresholds. Used
// when no per-repo issue_noise_config row exists.
func DefaultThresholds() Thresholds {
	return Thresholds{
		SpamFloor:       0.700,
		LowQualityFloor: 0.400,
		DuplicateFloor:  0.850,
		HoldTTL:         14 * 24 * time.Hour,
	}
}

// Decide maps a Score onto a Verdict given the active Thresholds.
// Pure function: no clock, no I/O, no allocation beyond the return.
//
// Precedence (per spec): spam > duplicate > low_quality > normal.
//
//   - spam wins because spam is "drop"; the cost of a false-positive
//     spam call is recoverable via maintainer release.
//   - duplicate wins over low_quality because the parent issue already
//     exists and merging signal is more useful than holding twice.
func Decide(s Score, t Thresholds) Verdict {
	if s.Spam >= t.SpamFloor {
		return VerdictSpam
	}
	if s.Duplicate >= t.DuplicateFloor {
		return VerdictDuplicate
	}
	if s.LowQuality >= t.LowQualityFloor {
		return VerdictLowQuality
	}
	return VerdictNormal
}
