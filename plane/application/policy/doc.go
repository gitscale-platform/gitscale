// Package policy is the application-plane plan-approval policy engine. It
// defines the Policy DSL, evaluates policy predicates against agent-proposed
// plans, records decisions on a Merkle-chained audit log, and exposes the
// Engine interface that gates state-mutating actions behind the
// human-oversight ladder configured per org and repo.
//
// The package is the safety boundary between agent autonomy and human
// oversight described in ADR-015. Every Policy mutation, every Plan status
// transition, and every audit append must occur in a single SQL transaction
// alongside the corresponding outbox row (ADR-008). Workflow-plane callers
// reach the Engine over gRPC; in-process callers (REST handlers) inject the
// Engine concrete and call its methods directly.
//
// PredicateKind is a closed enum. Adding a kind requires a migration plus an
// ADR amendment — see ADR-015 for the rule-based-only constraint.
//
// HumanUser is the only principal kind allowed in an ApproverGroup
// (ADR-015): agents cannot approve plans, even on policies that nominally
// allow agent-decision approvers.
package policy
