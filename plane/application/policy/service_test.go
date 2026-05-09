package policy

import (
	"errors"
	"testing"
)

func TestAPIError_IsCode(t *testing.T) {
	err := NewAPIError(CodePlanAlreadyDecided, "x")
	if !IsAPICode(err, CodePlanAlreadyDecided) {
		t.Fatal("IsAPICode should match")
	}
	if IsAPICode(err, CodePlanExpired) {
		t.Fatal("IsAPICode false-positive")
	}
	if IsAPICode(errors.New("not an APIError"), CodePlanAlreadyDecided) {
		t.Fatal("IsAPICode should not match plain error")
	}
	if IsAPICode(nil, CodePlanAlreadyDecided) {
		t.Fatal("IsAPICode(nil) should be false")
	}
}

func TestAPIError_ErrorMessage(t *testing.T) {
	e := NewAPIError(CodeDecisionUnauthorized, "not in approver group")
	want := "policy: decision_unauthorized: not in approver group"
	if e.Error() != want {
		t.Errorf("got %q want %q", e.Error(), want)
	}
}
