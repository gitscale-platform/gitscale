// Package gitaly provides Gitaly-backed implementations of interfaces
// consumed by the application plane. It does NOT import any
// plane/application/... package; the production wiring layer
// instantiates GitalyBlobReader and passes it to the AGENTS.md hook
// adapter, where it is bound by structural typing to the adapter's
// BlobReader interface (see ADR-019, plane boundary one-way rule).
//
// This package depends only on plane/git/client (gRPC connection pool),
// plane/git/locator (repo→file-server resolution), and the Gitaly proto
// stubs.
package gitaly
