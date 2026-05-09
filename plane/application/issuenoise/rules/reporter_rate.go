package rules

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ReporterRateThreshold is the per-hour issue-creation count above
// which a reporter is flagged as spammy. 20/h is roughly one issue
// every three minutes — well above any legitimate human cadence and
// at the high end of agent cadence for healthy fleets.
const ReporterRateThreshold = 20

// ReporterRateSpamWeight is the spam contribution when fired.
const ReporterRateSpamWeight = 0.50

// ReporterRateCounter is the dependency the rule pulls from. Real
// implementations are backed by Redis (counter keyed by reporter_id +
// hour bucket). Tests inject a fake. Returns the current hour's count
// for reporter (NOT including the in-flight issue).
type ReporterRateCounter interface {
	Count(ctx context.Context, reporterID uuid.UUID) (int, error)
}

// ReporterRate returns a rule closure that consults c. Higher-order
// shape lets the scorer wire dependencies once at construction time.
func ReporterRate(c ReporterRateCounter) Rule {
	return func(ctx context.Context, in Input) (Result, error) {
		if c == nil {
			return Result{}, nil
		}
		n, err := c.Count(ctx, in.ReporterID)
		if err != nil {
			// Cache miss / Redis blip: fail-soft. Returning an error
			// would propagate up to the scorer-level fail-open path
			// and silently skew toward "normal." Returning an empty
			// signal is the same outcome at lower noise.
			return Result{}, nil
		}
		if n < ReporterRateThreshold {
			return Result{}, nil
		}
		return Result{
			Category: CategorySpam,
			Signal: Signal{
				Name:   "reporter_rate",
				Weight: ReporterRateSpamWeight,
				Detail: fmt.Sprintf("reporter_count_1h=%d (threshold=%d)", n, ReporterRateThreshold),
			},
		}, nil
	}
}
