package ci

import (
	"testing"
)

// TestAssignTier_TruthTable enforces the spec routing rule with a closed
// truth table. Every (kind, annotation) tuple has exactly one expected
// Tier; the test fails if any row drifts. assignTier is the only piece
// of policy logic inside the workflow body — the truth table here is the
// authoritative regression surface.
func TestAssignTier_TruthTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		kind PrincipalKind
		ann  map[string]string
		want Tier
	}{
		{"agent_default_cold", PrincipalAgent, nil, TierCold},
		{"agent_with_require_hot_pool_overrides_to_hot", PrincipalAgent,
			map[string]string{AnnotationRequireHotPool: "true"}, TierHot},
		{"agent_with_require_hot_pool_false_stays_cold", PrincipalAgent,
			map[string]string{AnnotationRequireHotPool: "false"}, TierCold},
		{"agent_with_require_hot_pool_garbage_stays_cold", PrincipalAgent,
			map[string]string{AnnotationRequireHotPool: "yes"}, TierCold},
		{"human_default_hot", PrincipalHuman, nil, TierHot},
		{"human_with_require_hot_pool_stays_hot", PrincipalHuman,
			map[string]string{AnnotationRequireHotPool: "true"}, TierHot},
		{"service_default_hot", PrincipalService, nil, TierHot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assignTier(tc.kind, tc.ann)
			if got != tc.want {
				t.Fatalf("assignTier(%s, %v) = %s, want %s", tc.kind, tc.ann, got, tc.want)
			}
		})
	}
}

// TestSortedKeys_Deterministic asserts that sortedKeys returns a
// lexicographic ordering. The workflow body relies on this for any
// future predicate that needs to walk Annotations or Env keys without
// violating Temporal replay determinism.
func TestSortedKeys_Deterministic(t *testing.T) {
	t.Parallel()
	in := map[string]string{"z": "1", "a": "2", "m": "3"}
	got := sortedKeys(in)
	want := []string{"a", "m", "z"}
	if len(got) != len(want) {
		t.Fatalf("sortedKeys: got %d keys, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("sortedKeys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestPrincipalKind_String covers the wire-form serialisation used by
// the EmitUsageEvent payload.
func TestPrincipalKind_String(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind PrincipalKind
		want string
	}{
		{PrincipalUnknown, "unknown"},
		{PrincipalHuman, "human"},
		{PrincipalAgent, "agent"},
		{PrincipalService, "service"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("PrincipalKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// TestTier_String covers the wire-form serialisation written to the
// ci.job_completed outbox payload.
func TestTier_String(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tier Tier
		want string
	}{
		{TierUnknown, "unknown"},
		{TierHot, "hot"},
		{TierCold, "cold"},
	}
	for _, tc := range cases {
		if got := tc.tier.String(); got != tc.want {
			t.Errorf("Tier(%d).String() = %q, want %q", tc.tier, got, tc.want)
		}
	}
}
