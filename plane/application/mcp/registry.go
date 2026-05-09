package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/gitscale-platform/gitscale/plane/application/restapi"
)

// ToolHandler is the per-tool dispatch function. Receives the
// authenticated Principal (never re-parses the bearer) and the raw
// tool-call params; returns the JSON-serialisable result or an error.
type ToolHandler func(ctx context.Context, p restapi.Principal, params json.RawMessage) (any, error)

// Tool is a registered MCP tool. Name + Description + InputSchema are
// surfaced in `tools/list`; Handler is invoked on `tools/call`.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Handler     ToolHandler     `json:"-"`
}

// Registry is the closed map of tool names to Tool definitions.
// Construct one per server (NewRegistry) and call Register exactly
// once per tool name.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// ErrToolAlreadyRegistered is returned by Register when name is already
// in the registry. A duplicate registration is a programmer error;
// callers panic at startup rather than silently overwrite.
var ErrToolAlreadyRegistered = errors.New("mcp: tool already registered")

// Register inserts t into the registry. Returns ErrToolAlreadyRegistered
// if the name is already present.
func (r *Registry) Register(t Tool) error {
	if t.Name == "" {
		return errors.New("mcp: empty tool name")
	}
	if t.Handler == nil {
		return fmt.Errorf("mcp: tool %q has nil handler", t.Name)
	}
	if _, dup := r.tools[t.Name]; dup {
		return fmt.Errorf("%w: %s", ErrToolAlreadyRegistered, t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// MustRegister is the panicking variant; suitable inside
// RegisterDefaults where a duplicate is a build-time bug.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get returns the Tool for name, or (Tool{}, false) when absent.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Manifest returns the JSON-serialisable list of tools sorted by name
// for deterministic `tools/list` output. Stable order is part of the
// public contract: clients can hash the manifest for capability
// discovery.
func (r *Registry) Manifest() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names lists the registered tool names sorted alphabetically. Used by
// tests to assert exhaustive registration.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
