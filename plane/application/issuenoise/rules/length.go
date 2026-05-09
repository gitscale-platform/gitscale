package rules

import (
	"context"
	"fmt"
)

const (
	// LengthFloor is the minimum acceptable body length in chars.
	// Bodies below this are flagged low_quality (the issue likely
	// lacks reproduction steps / context).
	LengthFloor = 30
	// LengthCeiling is the maximum acceptable body length in chars.
	// Bodies above this are flagged low_quality (often paste-bombed
	// stack traces or model dumps that should be attached, not
	// inlined).
	LengthCeiling = 50 * 1024 // 50 KB
	// LengthWeight is the low_quality contribution when fired.
	LengthWeight = 0.40
)

// Length flags bodies that are either too short or too long. Both
// failure modes contribute LengthWeight to low_quality.
func Length(_ context.Context, in Input) (Result, error) {
	n := len(in.Body)
	if n >= LengthFloor && n <= LengthCeiling {
		return Result{}, nil
	}
	detail := fmt.Sprintf("body=%d chars (floor=%d, ceiling=%d)", n, LengthFloor, LengthCeiling)
	return Result{
		Category: CategoryLowQuality,
		Signal: Signal{
			Name:   "length",
			Weight: LengthWeight,
			Detail: detail,
		},
	}, nil
}
