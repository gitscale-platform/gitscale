// Package billing is the application-plane billing domain service. It exposes
// state-mutating operations (currently RecordPartitionArchived) that perform
// source-row + outbox-row writes in a single transaction (ADR-008). Workflow-
// plane callers reach this package over gRPC via plane/workflow/appclient
// (ADR-019); only the application plane talks to it in-process.
package billing
