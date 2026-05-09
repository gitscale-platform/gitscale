package rules

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ReputationLowQualityFloor is the reputation level below which the
// rule contributes to low_quality. Reputation is in [0.0, 1.0]; the
// platform default for a new agent is 0.5.
const ReputationLowQualityFloor = 0.30

// ReputationSpamFloor is the reputation level below which the rule
// contributes to spam. Hard floor; an agent at this level has been
// repeatedly downgraded.
const ReputationSpamFloor = 0.10

const (
	// ReputationLowQualityWeight is the low_quality contribution at
	// reputation < ReputationLowQualityFloor (and >= SpamFloor).
	ReputationLowQualityWeight = 0.25
	// ReputationSpamWeight is the spam contribution at reputation
	// < ReputationSpamFloor.
	ReputationSpamWeight = 0.50
)

// ReputationLookup is the reputation-source surface — the rule does
// not import the identity service directly; the scorer wires this in
// at construction time. ReporterID may be a human-user id (in which
// case the lookup returns 1.0 — humans bypass the reputation floor)
// or an agent-identity id.
type ReputationLookup interface {
	Score(ctx context.Context, reporterID uuid.UUID) (float64, error)
}

// Reputation returns a rule closure that consults l. On error from
// the lookup, the rule contributes nothing — fail-soft, same logic as
// reporter_rate.
//
// Precedence: spam-level reputation contributes ReputationSpamWeight
// to spam; low-quality-level contributes ReputationLowQualityWeight
// to low_quality; otherwise no contribution.
func Reputation(l ReputationLookup) Rule {
	return func(ctx context.Context, in Input) (Result, error) {
		if l == nil {
			return Result{}, nil
		}
		score, err := l.Score(ctx, in.ReporterID)
		if err != nil {
			return Result{}, nil
		}
		if score < ReputationSpamFloor {
			return Result{
				Category: CategorySpam,
				Signal: Signal{
					Name:   "reputation",
					Weight: ReputationSpamWeight,
					Detail: fmt.Sprintf("reputation=%.3f (spam_floor=%.2f)", score, ReputationSpamFloor),
				},
			}, nil
		}
		if score < ReputationLowQualityFloor {
			return Result{
				Category: CategoryLowQuality,
				Signal: Signal{
					Name:   "reputation",
					Weight: ReputationLowQualityWeight,
					Detail: fmt.Sprintf("reputation=%.3f (low_quality_floor=%.2f)", score, ReputationLowQualityFloor),
				},
			}, nil
		}
		return Result{}, nil
	}
}
