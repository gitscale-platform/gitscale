package runner

import (
	"errors"
	"testing"
)

// TestValidateAgainstQuota covers the per-job-ceiling derivation.
func TestValidateAgainstQuota(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		shape   ResourceShape
		monthly int64
		wantErr error
	}{
		{
			name:    "free_tier_default_shape_within_ceiling",
			shape:   ResourceShape{VCPU: 2, MemoryMB: 2048, WallClockSeconds: 1800},
			monthly: 500, // free tier
			wantErr: nil,
		},
		{
			name:    "zero_monthly_cap_rejects",
			shape:   ResourceShape{VCPU: 2, MemoryMB: 2048, WallClockSeconds: 1800},
			monthly: 0,
			wantErr: ErrQuotaInsufficient,
		},
		{
			name:    "negative_monthly_cap_rejects",
			shape:   ResourceShape{VCPU: 2, MemoryMB: 2048, WallClockSeconds: 1800},
			monthly: -1,
			wantErr: ErrQuotaInsufficient,
		},
		{
			name:    "zero_vcpu_invalid_shape",
			shape:   ResourceShape{VCPU: 0, MemoryMB: 2048, WallClockSeconds: 1800},
			monthly: 500,
			wantErr: ErrInvalidShape,
		},
		{
			name:    "zero_memory_invalid_shape",
			shape:   ResourceShape{VCPU: 2, MemoryMB: 0, WallClockSeconds: 1800},
			monthly: 500,
			wantErr: ErrInvalidShape,
		},
		{
			name:    "zero_wallclock_invalid_shape",
			shape:   ResourceShape{VCPU: 2, MemoryMB: 2048, WallClockSeconds: 0},
			monthly: 500,
			wantErr: ErrInvalidShape,
		},
		{
			name:    "huge_shape_exceeds_ceiling",
			shape:   ResourceShape{VCPU: 16, MemoryMB: 32 * 1024, WallClockSeconds: 7200},
			monthly: 500, // free: ceiling = 50 vcpu-min = 3000 vcpu-sec
			wantErr: ErrQuotaInsufficient,
		},
		{
			name:    "enterprise_tier_huge_shape_within_ceiling",
			shape:   ResourceShape{VCPU: 16, MemoryMB: 32 * 1024, WallClockSeconds: 1800},
			monthly: 100000, // ceiling = 600000 vcpu-sec; ask = 28800 → ok
			wantErr: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAgainstQuota(tc.shape, tc.monthly)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("validateAgainstQuota: got %v, want %v", err, tc.wantErr)
			}
		})
	}
}
