package rules

import (
	"context"
	"strings"
	"testing"
)

func TestLinkDensity(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		body      string
		wantFires bool
	}{
		{"empty", "", false},
		{"short_body_with_url_skipped", "see https://x.com", false},
		{"plain_text_no_urls", strings.Repeat("a normal bug report with no links. ", 10), false},
		{"single_url_below_threshold", strings.Repeat("background context. ", 30) + " https://example.com/issue/1", false},
		{"many_urls_high_density", strings.Repeat("https://x ", 20), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := LinkDensity(ctx, Input{Body: tc.body})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			fired := r.Signal.Weight > 0
			if fired != tc.wantFires {
				t.Fatalf("fired=%v want %v (signal=%+v)", fired, tc.wantFires, r.Signal)
			}
			if fired && r.Category != CategorySpam {
				t.Errorf("expected category=spam, got %v", r.Category)
			}
		})
	}
}
