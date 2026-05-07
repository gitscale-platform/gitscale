// Package billing contains workflow-plane code for billing-domain
// maintenance: monthly partition rollover (#18-rollover) and (future)
// archive (#18-archive). All workflows here are deterministic per ADR-003;
// activities reach into plane/data/store/billing.Partitioner — a DDL-only
// surface exempt from the app-plane routing rule (ADR-019, no outbox row).
package billing
