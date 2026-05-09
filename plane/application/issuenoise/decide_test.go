package issuenoise

import "testing"

func TestDecide_Precedence(t *testing.T) {
	th := DefaultThresholds()

	cases := []struct {
		name string
		s    Score
		want Verdict
	}{
		{"normal_zero", Score{}, VerdictNormal},
		{"normal_below_floors", Score{Spam: 0.5, LowQuality: 0.39, Duplicate: 0.84}, VerdictNormal},
		{"low_quality_only", Score{LowQuality: 0.4}, VerdictLowQuality},
		{"duplicate_beats_low_quality", Score{LowQuality: 0.95, Duplicate: 0.85}, VerdictDuplicate},
		{"spam_beats_all", Score{Spam: 0.7, Duplicate: 0.99, LowQuality: 0.99}, VerdictSpam},
		{"spam_at_floor", Score{Spam: 0.7}, VerdictSpam},
		{"duplicate_below_floor_falls_back", Score{LowQuality: 0.5, Duplicate: 0.84}, VerdictLowQuality},
		{"spam_below_floor", Score{Spam: 0.69}, VerdictNormal},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.s, th)
			if got != tc.want {
				t.Fatalf("Decide(%+v) = %s, want %s", tc.s, got, tc.want)
			}
		})
	}
}

func TestVerdict_String(t *testing.T) {
	cases := map[Verdict]string{
		VerdictUnknown:    "unknown",
		VerdictNormal:     "normal",
		VerdictLowQuality: "low_quality",
		VerdictDuplicate:  "duplicate",
		VerdictSpam:       "spam",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Errorf("Verdict(%d).String() = %q, want %q", v, got, want)
		}
	}
}

func TestVerdict_Helpers(t *testing.T) {
	if !VerdictSpam.IsTerminal() {
		t.Error("spam must be terminal")
	}
	if VerdictLowQuality.IsTerminal() {
		t.Error("low_quality must not be terminal")
	}
	if !VerdictLowQuality.IsHeld() || !VerdictDuplicate.IsHeld() {
		t.Error("low_quality and duplicate must be held")
	}
	if VerdictNormal.IsHeld() || VerdictSpam.IsHeld() {
		t.Error("normal and spam must not be held")
	}
}
