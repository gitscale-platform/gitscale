// Package kafka defines Kafka topic name constants and shared types used by
// the outbox consumer and downstream event consumers (ADR-004, ADR-008).
//
// Topic naming convention: gitscale.<domain>.events
// Partition key: aggregate_id (ADR-004, amended 2026-05-04).
package kafka

const (
	// TopicIdentityEvents is the Kafka topic for the identity domain outbox.
	TopicIdentityEvents = "gitscale.identity.events"

	// TopicRepositoriesEvents is the Kafka topic for the repositories domain outbox.
	TopicRepositoriesEvents = "gitscale.repositories.events"

	// TopicCollaborationEvents is the Kafka topic for the collaboration domain outbox.
	TopicCollaborationEvents = "gitscale.collaboration.events"

	// TopicCIEvents is the Kafka topic for the ci domain outbox.
	TopicCIEvents = "gitscale.ci.events"

	// TopicBillingEvents is the Kafka topic for the billing domain outbox.
	TopicBillingEvents = "gitscale.billing.events"
)
