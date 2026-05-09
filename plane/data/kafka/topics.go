// Package kafka defines the Kafka topic and consumer-group name constants for
// the GitScale event bus, the EventEnvelope type, and topology helpers.
//
// Partition key for every topic is aggregate_id (UUID) per ADR-004.
// Events reach Kafka only through the polling outbox consumer per ADR-008.
// No package in this tree imports a concrete Kafka driver — producers and
// consumers wire themselves at startup and reference these constants only.
package kafka

// Topic name constants — single source of truth for string literals used
// across the outbox consumer, Terraform data source, and topology apply CLI.
//
// Naming convention: gitscale.<domain>.events[.dlq]
const (
	TopicIdentityEvents    = "gitscale.identity.events"
	TopicIdentityEventsDLQ = "gitscale.identity.events.dlq"

	TopicRepositoriesEvents    = "gitscale.repositories.events"
	TopicRepositoriesEventsDLQ = "gitscale.repositories.events.dlq"

	TopicCollaborationEvents    = "gitscale.collaboration.events"
	TopicCollaborationEventsDLQ = "gitscale.collaboration.events.dlq"

	TopicCIEvents    = "gitscale.ci.events"
	TopicCIEventsDLQ = "gitscale.ci.events.dlq"

	TopicBillingEvents    = "gitscale.billing.events"
	TopicBillingEventsDLQ = "gitscale.billing.events.dlq"

	// Git metering events feed the ADR-015 reconciliation path. Drained by
	// the existing outbox consumer; consumed downstream by the analytics
	// sink (ClickHouse in production; an in-memory stub for now).
	TopicGitMeteringEvents    = "gitscale.git.metering.events"
	TopicGitMeteringEventsDLQ = "gitscale.git.metering.events.dlq"
)

// AllMainTopics lists every domain main topic (no DLQs).
// Useful for consumers that subscribe to all domains (e.g. SearchIndexer, AuditLog).
var AllMainTopics = []string{
	TopicIdentityEvents,
	TopicRepositoriesEvents,
	TopicCollaborationEvents,
	TopicCIEvents,
	TopicBillingEvents,
	TopicGitMeteringEvents,
}
