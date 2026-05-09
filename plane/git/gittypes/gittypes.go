// Package gittypes carries the value types shared across plane/git
// subpackages (rpc, hook, metering, locator). Keeping these types in a
// leaf package avoids the import cycle that would otherwise form between
// rpc and the components it composes (hook, metering).
package gittypes

// RepoRef identifies a repository for a Git operation.
// AgentID is empty for human (non-agent) operations; metering distinguishes
// agent vs human traffic on this field.
type RepoRef struct {
	RepoID  string // UUID string
	AgentID string
}

// RefUpdate is a single ref change in a push. The proxy receives parsed
// updates from the SSH/HTTP adapter and forwards them to the HookHandler
// before opening the Gitaly stream.
type RefUpdate struct {
	RefName string
	OldOID  string
	NewOID  string
}
