// Package cirunclient is the application-plane Temporal client wrapper
// for triggering CI runs from the MCP `ci_trigger` tool (#112).
//
// Plane boundary (ADR-019): this package imports go.temporal.io/sdk/client
// only. It MUST NOT import any plane/workflow/... package — workflow
// definitions are the workflow plane's concern. The workflow type name
// "CIRunWorkflow" is wired by reference here as a string literal; the
// workflow plane registers a worker for that name independently.
package cirunclient
