package billing

import (
	"context"
	"errors"
	"testing"
)

type stubChecker struct {
	held   bool
	reason string
	err    error
}

func (s stubChecker) IsHeld(_ context.Context, _ string) (bool, string, error) {
	return s.held, s.reason, s.err
}

func TestCheckLegalHoldActivity_PropagatesVerdict(t *testing.T) {
	a, err := NewCheckLegalHoldActivity(stubChecker{held: true, reason: "litigation"})
	if err != nil {
		t.Fatalf("NewCheckLegalHoldActivity: %v", err)
	}
	res, err := a.Execute(context.Background(), LegalHoldCheckInput{LakeURI: "s3://b/k"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Held || res.Reason != "litigation" {
		t.Errorf("verdict=%+v", res)
	}
}

func TestCheckLegalHoldActivity_PropagatesError(t *testing.T) {
	a, _ := NewCheckLegalHoldActivity(stubChecker{err: errors.New("aws: 500")})
	if _, err := a.Execute(context.Background(), LegalHoldCheckInput{}); err == nil {
		t.Error("expected error")
	}
}

func TestCheckLegalHoldActivity_NilChecker(t *testing.T) {
	if _, err := NewCheckLegalHoldActivity(nil); err == nil {
		t.Error("expected error for nil checker")
	}
}

func TestStaticLegalHoldChecker(t *testing.T) {
	c := NewStaticLegalHoldChecker(false, "")
	held, _, err := c.IsHeld(context.Background(), "s3://b/k")
	if err != nil || held {
		t.Errorf("static checker returned held=%v err=%v", held, err)
	}
}
