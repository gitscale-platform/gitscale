package rules

import (
	"context"
	"fmt"
	"unicode"
)

// LanguageNonASCIICeiling is the fraction of non-ASCII-letter runes
// above which the body is flagged. Set conservatively at 0.6 — issues
// in languages like Japanese, Korean, Cyrillic legitimately exceed this.
// In v1 this rule is a low-confidence signal: it only fires when both
// the non-ASCII ratio is high AND the body has very few ASCII letters
// at all (heuristic for "no English content"). Operators can tune this
// via the rule registry once usage data accrues.
const LanguageNonASCIICeiling = 0.6

// LanguageMinASCIILetters is the floor on ASCII letter count below
// which the rule treats the body as non-target-language regardless of
// the ratio. Prevents 3-char bodies of latin letters from being scored
// as English.
const LanguageMinASCIILetters = 5

// LanguageWeight is the low_quality contribution when fired.
const LanguageWeight = 0.20

// Language is a heuristic language-acceptability check. It fires when
// the body appears to contain very little ASCII-letter content — a
// proxy for "not in the repo's target language." This is intentionally
// crude in v1; a proper detector (e.g. lingua-go) is a follow-up.
//
// Returns CategoryLowQuality on fire.
func Language(_ context.Context, in Input) (Result, error) {
	body := in.Body
	if len(body) == 0 {
		return Result{}, nil
	}
	var asciiLetters, totalLetters int
	for _, r := range body {
		if !unicode.IsLetter(r) {
			continue
		}
		totalLetters++
		if r < 128 {
			asciiLetters++
		}
	}
	if totalLetters == 0 {
		return Result{}, nil
	}
	nonASCIIRatio := 1.0 - float64(asciiLetters)/float64(totalLetters)
	if nonASCIIRatio < LanguageNonASCIICeiling || asciiLetters >= LanguageMinASCIILetters {
		return Result{}, nil
	}
	return Result{
		Category: CategoryLowQuality,
		Signal: Signal{
			Name:   "language",
			Weight: LanguageWeight,
			Detail: fmt.Sprintf("ascii_letters=%d non_ascii_ratio=%.2f", asciiLetters, nonASCIIRatio),
		},
	}, nil
}
