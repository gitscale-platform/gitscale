// Package hook defines the in-process pre-receive hook contract invoked by
// the GitalyProxy before a push is forwarded to Gitaly. The default
// implementation is NoopHookHandler; AGENTS.md enforcement (#114) and
// metering reconciliation (#109) wire concrete implementations in later
// PRs.
package hook

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/git/gittypes"
)

// HookHandler is called synchronously inside ReceivePack before the push is
// forwarded to Gitaly. A non-nil error rejects the push; the proxy converts
// the error to gRPC PermissionDenied and surfaces the message to the Git
// client.
//
// Implementations must be safe for concurrent use and must not block
// indefinitely; the hook is on the push hot path.
type HookHandler interface {
	PreReceive(ctx context.Context, repo gittypes.RepoRef, updates []gittypes.RefUpdate) error
}
