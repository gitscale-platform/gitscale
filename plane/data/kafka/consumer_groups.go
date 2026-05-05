package kafka

// Consumer group name constants.
//
// Each constant is annotated with:
//   - the topics it subscribes to
//   - the SPIFFE workload identity of the consumer service (ADR-010)
//
// Full Terraform ACL wiring is future work; SPIFFE IDs here match the
// acls.consumer_spiffe_ids declared in topics.yaml so tooling can cross-check.
const (
	// GroupSearchIndexer consumes ALL 5 main topics.
	// Indexes events into Vespa for customer-facing search (ADR-016).
	// SPIFFE ID: spiffe://gitscale.dev/ns/data/sa/search-indexer
	GroupSearchIndexer = "gitscale.search-indexer"

	// GroupAuditLog consumes ALL 5 main topics.
	// Writes immutable audit records to ClickHouse (the durable audit store per ADR-008).
	// SPIFFE ID: spiffe://gitscale.dev/ns/data/sa/audit-log
	GroupAuditLog = "gitscale.audit-log"

	// GroupWebhookFanout consumes repositories.events, collaboration.events, ci.events.
	// Fans out to customer-configured webhook endpoints.
	// SPIFFE ID: spiffe://gitscale.dev/ns/data/sa/webhook-fanout
	GroupWebhookFanout = "gitscale.webhook-fanout"

	// GroupBillingAggregator consumes billing.events.
	// Aggregates usage events into customer invoice line items.
	// Uses envelope.RateBucket for routing to the correct billing tier.
	// SPIFFE ID: spiffe://gitscale.dev/ns/data/sa/billing-aggregator
	GroupBillingAggregator = "gitscale.billing-aggregator"

	// GroupColdStorageMigrator consumes repositories.events.
	// Learns which repos have crossed the hot→cold boundary (last_active_at > 30d)
	// and triggers erasure-coding migration jobs in the workflow plane (ADR-001).
	// SPIFFE ID: spiffe://gitscale.dev/ns/data/sa/cold-storage-migrator
	GroupColdStorageMigrator = "gitscale.cold-storage-migrator"
)

// DefaultAutoOffsetReset is the Kafka auto.offset.reset setting for all
// consumer groups. Late-binding consumers must backfill from the beginning,
// not skip events they have not yet seen (D7 per issue #12 spec).
const DefaultAutoOffsetReset = "earliest"
