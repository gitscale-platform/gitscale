// Package outboxttl is the Temporal workflow + activity that periodically
// expires processed outbox rows across all five GitScale domains, per
// ADR-008. The workflow fans out to one ExpireDomainOutboxActivity per
// domain (identity, repositories, collaboration, ci, billing) and each
// activity drives a domain-specific *outbox.Expirer that DELETEs rows
// whose processed_at is older than the configured TTL (default 24h).
//
// Determinism: the workflow body iterates a fixed-order slice and awaits
// futures in declaration order. No time.Now / map-iter / random / goroutine
// calls inside the workflow function — all side effects are confined to
// the activity (ADR-003).
package outboxttl
