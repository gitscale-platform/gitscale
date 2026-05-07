// Package workflow holds the GitScale workflow-plane bootstrap shared by
// every Temporal workflow in the platform: task-queue constants, registration
// scaffolding (Bundle), and the lint contract enforcing determinism on
// workflow code (ADR-003, ADR-019).
//
// Task queues are partitioned by workload class — billing maintenance, agent
// sessions, CI pipelines — not by tenant or domain. Per-tenant queues are an
// anti-pattern at Temporal scale; tenant isolation is workflow-id-prefix +
// search-attributes territory (Phase 2).
//
// State-mutating activities route through plane/workflow/appclient/<domain>
// gRPC clients into the application plane (ADR-019). Read-only activities
// MAY use plane/data/store and plane/data/cache interfaces directly. Pure-
// DDL maintenance activities (e.g. CreatePartition) are exempt from the
// app-plane routing rule because they have no outbox row.
package workflow

// Task queue constants. Bundles register against one of these per worker.
// Adding a new queue is rare — coarse-by-workload routing is intentional.
const (
	// QueueBillingMaintenance hosts billing.usage_events partition rollover
	// (#18) and future operational-analytics maintenance jobs.
	QueueBillingMaintenance = "billing-maintenance"

	// QueueAgentSessions hosts long-running agent-session lifecycle
	// workflows. Reserved for the agent runtime epic (Phase 2).
	QueueAgentSessions = "agent-sessions"

	// QueueCIPipelines hosts CI runner provisioning workflows (Firecracker
	// microVM lifecycle). Reserved for the CI epic (Phase 2).
	QueueCIPipelines = "ci-pipelines"
)

// AllQueues is the canonical list of currently-defined task queues.
var AllQueues = []string{
	QueueBillingMaintenance,
	QueueAgentSessions,
	QueueCIPipelines,
}
