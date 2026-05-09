package policy

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Validation error codes. These are CLOSED enum strings stable across the
// gRPC and REST surfaces; consumers map them to user-facing copy. Adding a
// code requires updating the API error envelope spec in tandem.
const (
	CodeInvalidPredicateKind  = "policy_invalid_predicate_kind"
	CodeEmptyLadder           = "policy_empty_ladder"
	CodeUnknownApproverGroup  = "policy_unknown_approver_group"
	CodeZeroRequiredCount     = "policy_zero_required_count"
	CodeNonHumanApprover      = "policy_non_human_approver"
	CodeInvalidOnTimeout      = "policy_invalid_on_timeout"
	CodeBulkThresholdMissing  = "policy_bulk_threshold_missing"
	CodeRequiredCountTooLarge = "policy_required_count_too_large"
	CodeFallBackOnFirstRung   = "policy_fall_back_on_first_rung"
	CodeEmptyName             = "policy_empty_name"
	CodeNonPositiveExpiry     = "policy_non_positive_expiry"
	CodeNonPositiveSLA        = "policy_non_positive_sla"
	CodeNoRules               = "policy_no_rules"
)

// ValidationError carries a closed-enum Code and a contextual message. The
// engine returns the first failure it observes; callers that need full
// diagnostics should re-validate after fixing the first error.
type ValidationError struct {
	Code    string
	Message string
}

// Error implements error.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("policy: %s: %s", e.Code, e.Message)
}

// IsCode reports whether err (or any wrapped error) is a ValidationError
// whose Code equals code. Useful in tests and at the gRPC error-mapping
// layer.
func IsCode(err error, code string) bool {
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	return ve.Code == code
}

// Validate runs the full set of structural checks on p. It does NOT verify
// HMAC signatures (that lives in the store layer where the org KEK is in
// scope) and does NOT confirm that referenced HumanUserIDs exist in the
// identity domain (that is a defence-in-depth check at write time).
//
// A nil return means p is internally consistent and safe to pass to the
// engine. The first error encountered is returned; ordering is stable.
func Validate(p *Policy) error {
	if p == nil {
		return &ValidationError{Code: CodeEmptyName, Message: "policy is nil"}
	}
	if p.Name == "" {
		return &ValidationError{Code: CodeEmptyName, Message: "policy.name is empty"}
	}
	if len(p.Rules) == 0 {
		return &ValidationError{Code: CodeNoRules, Message: "policy.rules is empty"}
	}

	// Validate approver groups before rules so ladder cross-references
	// resolve against a known-good map.
	for name, g := range p.ApproverGroups {
		if name != g.Name {
			return &ValidationError{
				Code:    CodeEmptyName,
				Message: fmt.Sprintf("approver group key %q does not match group.Name %q", name, g.Name),
			}
		}
		if g.RequiredCount <= 0 {
			return &ValidationError{
				Code:    CodeZeroRequiredCount,
				Message: fmt.Sprintf("approver group %q has required_count <= 0", name),
			}
		}
		if g.RequiredCount > len(g.HumanUserIDs) {
			return &ValidationError{
				Code:    CodeRequiredCountTooLarge,
				Message: fmt.Sprintf("approver group %q required_count exceeds member count", name),
			}
		}
		// ADR-015: every member must be a HumanUser. We can only check the
		// shape of the slice (no nil UUIDs); identity-kind verification
		// happens at write time when the identity reader is in scope.
		for _, id := range g.HumanUserIDs {
			if id == uuid.Nil {
				return &ValidationError{
					Code:    CodeNonHumanApprover,
					Message: fmt.Sprintf("approver group %q contains a zero UUID", name),
				}
			}
		}
	}

	// Validate each rule.
	for i, r := range p.Rules {
		if !r.Kind.IsValid() {
			return &ValidationError{
				Code:    CodeInvalidPredicateKind,
				Message: fmt.Sprintf("rule[%d]: predicate kind %q is not in the closed enum", i, r.Kind),
			}
		}
		if r.ExpirySeconds <= 0 {
			return &ValidationError{
				Code:    CodeNonPositiveExpiry,
				Message: fmt.Sprintf("rule[%d]: expiry_seconds must be > 0", i),
			}
		}
		if r.Kind == PredicateBulkAction && (r.Threshold == nil || *r.Threshold <= 0) {
			return &ValidationError{
				Code:    CodeBulkThresholdMissing,
				Message: fmt.Sprintf("rule[%d]: bulk_action requires a positive threshold", i),
			}
		}
		if len(r.Ladder) == 0 {
			return &ValidationError{
				Code:    CodeEmptyLadder,
				Message: fmt.Sprintf("rule[%d]: ladder must contain at least one rung", i),
			}
		}
		for j, rung := range r.Ladder {
			if _, ok := p.ApproverGroups[rung.GroupName]; !ok {
				return &ValidationError{
					Code:    CodeUnknownApproverGroup,
					Message: fmt.Sprintf("rule[%d].ladder[%d]: group %q not declared in approver_groups", i, j, rung.GroupName),
				}
			}
			if rung.SLASeconds <= 0 {
				return &ValidationError{
					Code:    CodeNonPositiveSLA,
					Message: fmt.Sprintf("rule[%d].ladder[%d]: sla_seconds must be > 0", i, j),
				}
			}
			if !rung.OnTimeout.IsValid() {
				return &ValidationError{
					Code:    CodeInvalidOnTimeout,
					Message: fmt.Sprintf("rule[%d].ladder[%d]: on_timeout %q is not in the closed enum", i, j, rung.OnTimeout),
				}
			}
			if j == 0 && rung.OnTimeout == OnTimeoutFallBack {
				return &ValidationError{
					Code:    CodeFallBackOnFirstRung,
					Message: fmt.Sprintf("rule[%d].ladder[0]: first rung cannot use on_timeout=fall_back", i),
				}
			}
		}
	}
	return nil
}

