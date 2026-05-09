package rules

import (
	"context"
	"fmt"
	"regexp"
)

// LinkDensityThreshold is the (link-count / body-char) ratio at which
// the rule fires. 0.10 = roughly one URL per ten characters of body.
const LinkDensityThreshold = 0.10

// LinkDensityWeight is the spam contribution when the rule fires.
const LinkDensityWeight = 0.30

// urlPattern is intentionally simple — it matches http/https URLs and
// is deliberately not RFC 3986 strict because the rule tolerates false
// positives (over-matching makes the rule slightly more aggressive,
// which is acceptable for spam detection).
var urlPattern = regexp.MustCompile(`https?://[^\s<>"]+`)

// LinkDensity flags issues whose bodies have a high URL-to-character
// density. Returns CategorySpam with weight LinkDensityWeight when
// the threshold is exceeded; otherwise Result{} (no contribution).
//
// Bodies shorter than 20 chars never fire — the ratio is undefined for
// short text and would produce noise.
func LinkDensity(_ context.Context, in Input) (Result, error) {
	body := in.Body
	if len(body) < 20 {
		return Result{}, nil
	}
	matches := urlPattern.FindAllStringIndex(body, -1)
	if len(matches) == 0 {
		return Result{}, nil
	}
	density := float64(len(matches)) / float64(len(body))
	if density < LinkDensityThreshold {
		return Result{}, nil
	}
	return Result{
		Category: CategorySpam,
		Signal: Signal{
			Name:   "link_density",
			Weight: LinkDensityWeight,
			Detail: fmt.Sprintf("%d links / %d chars", len(matches), len(body)),
		},
	}, nil
}
