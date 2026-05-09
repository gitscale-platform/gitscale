package hook

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/git/gittypes"
)

// NoopHookHandler accepts every push unconditionally. It is the default
// HookHandler bound at proxy construction time. AGENTS.md enforcement
// replaces it via wiring in a follow-up PR (#114).
type NoopHookHandler struct{}

// PreReceive returns nil for all inputs.
func (NoopHookHandler) PreReceive(_ context.Context, _ gittypes.RepoRef, _ []gittypes.RefUpdate) error {
	return nil
}
