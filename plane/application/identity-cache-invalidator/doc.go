// Package invalidator implements the identity-cache-invalidator consumer:
// reads gitscale.identity.events from Kafka, dispatches by event_type, and
// deletes the cached identity entry for every UUID listed in the payload's
// affected_principal_ids[]. Idempotent on event_id via a Redis SET NX EX
// dedupe key with TTL matching Kafka retention (ADR-008, ADR-009).
//
// Cross-plane import rules (CLAUDE.md): this is application-plane code; it
// imports plane/data/cache and plane/data/kafka but never reaches into
// plane/edge or plane/git internals. The producer is plane/application/identity
// (#15-revocation, PR #64).
package invalidator
