package rules

import (
	"context"
	"testing"
)

func TestLanguage(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		body      string
		wantFires bool
	}{
		{"empty_no_fire", "", false},
		{"english_does_not_fire", "This bug repros when the cache is cold and the user clicks twice.", false},
		{"non_ascii_with_few_latin_letters_fires", "これはバグです です です です です", true},
		{"non_ascii_but_enough_latin_does_not_fire", "これは error message: NullPointerException at runtime", false},
		{"numbers_only_no_fire", "12345 67890", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Language(ctx, Input{Body: tc.body})
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			fired := r.Signal.Weight > 0
			if fired != tc.wantFires {
				t.Fatalf("fired=%v want %v signal=%+v", fired, tc.wantFires, r.Signal)
			}
		})
	}
}
