package main

import (
	"testing"
	"time"
)

func TestPriorMonthAnchor(t *testing.T) {
	cases := []struct {
		name        string
		year, month int
		wantY, wantM int
	}{
		{"mid-year", 2027, 5, 2027, 4},
		{"january rolls back to december", 2027, 1, 2026, 12},
		{"december", 2027, 12, 2027, 11},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := priorMonthAnchor(tc.year, tc.month)
			if got.Year() != tc.wantY || int(got.Month()) != tc.wantM || got.Day() != 15 {
				t.Fatalf("priorMonthAnchor(%d,%d)=%s want %d-%02d-15", tc.year, tc.month, got, tc.wantY, tc.wantM)
			}
			if got.Location() != time.UTC {
				t.Fatalf("anchor not UTC: %s", got.Location())
			}
		})
	}
}
