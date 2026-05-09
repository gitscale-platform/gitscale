package policy

import (
	"testing"

	"github.com/google/uuid"
)

// validPolicy returns a baseline-valid Policy that individual tests mutate
// to exercise a single failure path. Keeping the baseline isolated to a
// helper means each test asserts exactly one root cause.
func validPolicy() *Policy {
	approver := uuid.New()
	threshold := 50
	return &Policy{
		ID:      uuid.New(),
		OrgID:   uuid.New(),
		Name:    "default",
		Version: 1,
		ApproverGroups: map[string]ApproverGroup{
			"oncall": {Name: "oncall", HumanUserIDs: []uuid.UUID{approver}, RequiredCount: 1},
		},
		Rules: []Rule{
			{
				Kind:          PredicatePRMerge,
				Match:         map[string]string{"branch": "main"},
				ExpirySeconds: 86400,
				Ladder: []EscalationRung{
					{GroupName: "oncall", SLASeconds: 3600, OnTimeout: OnTimeoutAutoDeny},
				},
			},
			{
				Kind:          PredicateBulkAction,
				Threshold:     &threshold,
				ExpirySeconds: 86400,
				Ladder: []EscalationRung{
					{GroupName: "oncall", SLASeconds: 3600, OnTimeout: OnTimeoutNotifyNext},
					{GroupName: "oncall", SLASeconds: 1800, OnTimeout: OnTimeoutAutoDeny},
				},
			},
		},
	}
}

func TestValidate_Happy(t *testing.T) {
	if err := Validate(validPolicy()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidate_NilPolicy(t *testing.T) {
	if err := Validate(nil); !IsCode(err, CodeEmptyName) {
		t.Fatalf("want CodeEmptyName, got %v", err)
	}
}

func TestValidate_EmptyName(t *testing.T) {
	p := validPolicy()
	p.Name = ""
	if err := Validate(p); !IsCode(err, CodeEmptyName) {
		t.Fatalf("want CodeEmptyName, got %v", err)
	}
}

func TestValidate_NoRules(t *testing.T) {
	p := validPolicy()
	p.Rules = nil
	if err := Validate(p); !IsCode(err, CodeNoRules) {
		t.Fatalf("want CodeNoRules, got %v", err)
	}
}

func TestValidate_InvalidPredicateKind(t *testing.T) {
	p := validPolicy()
	p.Rules[0].Kind = PredicateKind("not_a_real_kind")
	if err := Validate(p); !IsCode(err, CodeInvalidPredicateKind) {
		t.Fatalf("want CodeInvalidPredicateKind, got %v", err)
	}
}

func TestValidate_NonPositiveExpiry(t *testing.T) {
	p := validPolicy()
	p.Rules[0].ExpirySeconds = 0
	if err := Validate(p); !IsCode(err, CodeNonPositiveExpiry) {
		t.Fatalf("want CodeNonPositiveExpiry, got %v", err)
	}
}

func TestValidate_BulkThresholdMissing(t *testing.T) {
	p := validPolicy()
	p.Rules[1].Threshold = nil
	if err := Validate(p); !IsCode(err, CodeBulkThresholdMissing) {
		t.Fatalf("want CodeBulkThresholdMissing, got %v", err)
	}
}

func TestValidate_EmptyLadder(t *testing.T) {
	p := validPolicy()
	p.Rules[0].Ladder = nil
	if err := Validate(p); !IsCode(err, CodeEmptyLadder) {
		t.Fatalf("want CodeEmptyLadder, got %v", err)
	}
}

func TestValidate_UnknownApproverGroup(t *testing.T) {
	p := validPolicy()
	p.Rules[0].Ladder[0].GroupName = "nope"
	if err := Validate(p); !IsCode(err, CodeUnknownApproverGroup) {
		t.Fatalf("want CodeUnknownApproverGroup, got %v", err)
	}
}

func TestValidate_NonPositiveSLA(t *testing.T) {
	p := validPolicy()
	p.Rules[0].Ladder[0].SLASeconds = 0
	if err := Validate(p); !IsCode(err, CodeNonPositiveSLA) {
		t.Fatalf("want CodeNonPositiveSLA, got %v", err)
	}
}

func TestValidate_InvalidOnTimeout(t *testing.T) {
	p := validPolicy()
	p.Rules[0].Ladder[0].OnTimeout = OnTimeout("explode")
	if err := Validate(p); !IsCode(err, CodeInvalidOnTimeout) {
		t.Fatalf("want CodeInvalidOnTimeout, got %v", err)
	}
}

func TestValidate_FallBackOnFirstRung(t *testing.T) {
	p := validPolicy()
	p.Rules[0].Ladder[0].OnTimeout = OnTimeoutFallBack
	if err := Validate(p); !IsCode(err, CodeFallBackOnFirstRung) {
		t.Fatalf("want CodeFallBackOnFirstRung, got %v", err)
	}
}

func TestValidate_ZeroRequiredCount(t *testing.T) {
	p := validPolicy()
	g := p.ApproverGroups["oncall"]
	g.RequiredCount = 0
	p.ApproverGroups["oncall"] = g
	if err := Validate(p); !IsCode(err, CodeZeroRequiredCount) {
		t.Fatalf("want CodeZeroRequiredCount, got %v", err)
	}
}

func TestValidate_RequiredCountTooLarge(t *testing.T) {
	p := validPolicy()
	g := p.ApproverGroups["oncall"]
	g.RequiredCount = 5 // only 1 member
	p.ApproverGroups["oncall"] = g
	if err := Validate(p); !IsCode(err, CodeRequiredCountTooLarge) {
		t.Fatalf("want CodeRequiredCountTooLarge, got %v", err)
	}
}

func TestValidate_NonHumanApprover_ZeroUUID(t *testing.T) {
	p := validPolicy()
	g := p.ApproverGroups["oncall"]
	g.HumanUserIDs = []uuid.UUID{uuid.Nil}
	p.ApproverGroups["oncall"] = g
	if err := Validate(p); !IsCode(err, CodeNonHumanApprover) {
		t.Fatalf("want CodeNonHumanApprover, got %v", err)
	}
}

func TestPredicateKind_IsValid(t *testing.T) {
	for _, k := range AllPredicateKinds() {
		if !k.IsValid() {
			t.Errorf("expected %q valid", k)
		}
	}
	if PredicateKind("").IsValid() {
		t.Error("empty kind reported valid")
	}
	if PredicateKind("nope").IsValid() {
		t.Error("garbage kind reported valid")
	}
}

func TestOnTimeout_IsValid(t *testing.T) {
	for _, v := range []OnTimeout{OnTimeoutNotifyNext, OnTimeoutAutoDeny, OnTimeoutFallBack} {
		if !v.IsValid() {
			t.Errorf("expected %q valid", v)
		}
	}
	if OnTimeout("").IsValid() {
		t.Error("empty on_timeout reported valid")
	}
}
