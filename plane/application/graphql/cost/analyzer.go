package cost

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// PrincipalKind discriminates per-class budgets. Mirrors the application's
// restapi/principal.go enum without importing it (cost is plane-pure).
type PrincipalKind int

const (
	PrincipalUnknown PrincipalKind = iota
	PrincipalHuman
	PrincipalAgent
)

// Cost is the analyzer's output for an accepted query.
type Cost struct {
	Depth      int
	Complexity int
}

// ParseCost is the cheaper bucket charge applied when a query is rejected
// pre-execution. Callers use it to deter probe-floods.
func ParseCost(c Cost) int {
	pc := c.Complexity / 10
	if pc < 20 {
		pc = 20
	}
	return pc
}

// Limits configures the analyzer. Zero values disable that limit.
type Limits struct {
	MaxDepth          map[PrincipalKind]int
	MaxComplexity     map[PrincipalKind]int
	PersistedDiscount float64 // e.g. 0.5
	DefaultFirst      int     // missing `first` falls back to this; default 20 if zero
	MaxFirst          int     // multipliers cap; default 100 if zero
}

// DefaultLimits returns the spec-table values: human=10/1000, agent=8/5000,
// persisted ×0.5, defaultFirst=20, maxFirst=100.
func DefaultLimits() Limits {
	return Limits{
		MaxDepth:          map[PrincipalKind]int{PrincipalHuman: 10, PrincipalAgent: 8},
		MaxComplexity:     map[PrincipalKind]int{PrincipalHuman: 1000, PrincipalAgent: 5000},
		PersistedDiscount: 0.5,
		DefaultFirst:      20,
		MaxFirst:          100,
	}
}

// FieldWeights is the per-field cost map. Connections with paginating
// arguments declare their multiplier names.
type FieldWeights struct {
	Default     int
	PerType     map[string]map[string]Weight // PerType[Type][Field] = Weight
}

// Weight is the cost contribution of a single field.
type Weight struct {
	// Weight is the base cost of selecting this field.
	Weight int
	// Multipliers is the list of argument names whose integer values
	// scale this field's cost. Typically `["first"]` for connections.
	Multipliers []string
}

// DefaultFieldWeights mirrors the @cost directives in schema.graphql. The
// directive is the source of truth at SDL level; this map is the analyzer's
// runtime mirror, kept intentionally explicit for review.
func DefaultFieldWeights() FieldWeights {
	return FieldWeights{
		Default: 1,
		PerType: map[string]map[string]Weight{
			"Query": {
				"repository":   {Weight: 1},
				"user":         {Weight: 1},
				"agent":        {Weight: 1},
				"pullRequest":  {Weight: 2},
				"organization": {Weight: 1},
			},
			"Repository": {
				"owner":        {Weight: 1},
				"pullRequests": {Weight: 2, Multipliers: []string{"first"}},
				"issues":       {Weight: 2, Multipliers: []string{"first"}},
			},
			"Organization": {
				"members": {Weight: 2, Multipliers: []string{"first"}},
			},
			"Mutation": {
				"createPullRequest":      {Weight: 10},
				"createAgent":            {Weight: 10},
				"updateAgentPermissions": {Weight: 10},
			},
		},
	}
}

// Analyzer enforces depth + complexity budgets pre-execution.
type Analyzer struct {
	Limits    Limits
	Weights   FieldWeights
	RootQuery string // typically "Query"
	RootMut   string // typically "Mutation"
}

// New returns an Analyzer with the supplied limits + weights, defaulting
// root type names to "Query"/"Mutation".
func New(lim Limits, w FieldWeights) *Analyzer {
	a := &Analyzer{Limits: lim, Weights: w, RootQuery: "Query", RootMut: "Mutation"}
	if a.Limits.DefaultFirst <= 0 {
		a.Limits.DefaultFirst = 20
	}
	if a.Limits.MaxFirst <= 0 {
		a.Limits.MaxFirst = 100
	}
	return a
}

// Sentinel errors. Wrapping a typed error keeps the router's mapErr switch
// exhaustive and machine-checkable.
var (
	ErrDepthExceeded       = errors.New("cost: depth exceeded")
	ErrCostBudgetExceeded  = errors.New("cost: complexity budget exceeded")
	ErrUnknownOperation    = errors.New("cost: unknown operation name")
	ErrAmbiguousOperation  = errors.New("cost: operationName required for multi-operation document")
	ErrFragmentCycle       = errors.New("cost: fragment cycle detected")
	ErrFragmentNotDefined  = errors.New("cost: fragment not defined")
)

// Analyze parses src, picks the operation matching opName (or the only
// one), and computes its cost under the supplied principal class.
//
// vars maps GraphQL variable names (without the leading `$`) to their
// integer-coercible runtime value. Only integer multipliers are read.
func (a *Analyzer) Analyze(src, opName string, vars map[string]any, kind PrincipalKind, persisted bool) (Cost, error) {
	doc, err := ParseQuery(src)
	if err != nil {
		return Cost{}, err
	}
	op, err := pickOperation(doc, opName)
	if err != nil {
		return Cost{}, err
	}
	rootType := a.RootQuery
	if op.Kind == OpMutation {
		rootType = a.RootMut
	}
	c := Cost{}
	visited := map[string]bool{}
	if err := a.walk(doc, op.Sels, rootType, 1, vars, visited, &c); err != nil {
		return Cost{}, err
	}
	if persisted && a.Limits.PersistedDiscount > 0 {
		c.Complexity = int(math.Ceil(float64(c.Complexity) * a.Limits.PersistedDiscount))
	}

	if maxD, ok := a.Limits.MaxDepth[kind]; ok && maxD > 0 && c.Depth > maxD {
		return c, fmt.Errorf("%w: depth=%d max=%d", ErrDepthExceeded, c.Depth, maxD)
	}
	if maxC, ok := a.Limits.MaxComplexity[kind]; ok && maxC > 0 && c.Complexity > maxC {
		return c, fmt.Errorf("%w: complexity=%d budget=%d", ErrCostBudgetExceeded, c.Complexity, maxC)
	}
	return c, nil
}

func pickOperation(doc *Document, name string) (Operation, error) {
	switch len(doc.Operations) {
	case 0:
		return Operation{}, fmt.Errorf("%w: empty document", ErrUnknownOperation)
	case 1:
		if name != "" && doc.Operations[0].Name != name && doc.Operations[0].Name != "" {
			return Operation{}, fmt.Errorf("%w: %q", ErrUnknownOperation, name)
		}
		return doc.Operations[0], nil
	}
	if name == "" {
		return Operation{}, ErrAmbiguousOperation
	}
	for _, op := range doc.Operations {
		if op.Name == name {
			return op, nil
		}
	}
	return Operation{}, fmt.Errorf("%w: %q", ErrUnknownOperation, name)
}

// walk traverses the selection set under parentType at depth d, summing
// complexity into c.
func (a *Analyzer) walk(doc *Document, sels []Selection, parentType string, d int, vars map[string]any, visited map[string]bool, c *Cost) error {
	if d > c.Depth {
		c.Depth = d
	}
	for _, s := range sels {
		switch s.Kind {
		case SelField:
			if err := a.walkField(doc, s.Field, parentType, d, vars, visited, c); err != nil {
				return err
			}
		case SelInlineFragment:
			child := s.Fragment.OnType
			if child == "" {
				child = parentType
			}
			if err := a.walk(doc, s.Fragment.Sels, child, d, vars, visited, c); err != nil {
				return err
			}
		case SelFragmentSpread:
			if visited[s.Fragment.Name] {
				return fmt.Errorf("%w: %s", ErrFragmentCycle, s.Fragment.Name)
			}
			frag, ok := doc.Fragments[s.Fragment.Name]
			if !ok {
				return fmt.Errorf("%w: %s", ErrFragmentNotDefined, s.Fragment.Name)
			}
			visited[s.Fragment.Name] = true
			if err := a.walk(doc, frag.Sels, frag.OnType, d, vars, visited, c); err != nil {
				return err
			}
			delete(visited, s.Fragment.Name)
		}
	}
	return nil
}

func (a *Analyzer) walkField(doc *Document, f Field, parentType string, d int, vars map[string]any, visited map[string]bool, c *Cost) error {
	// Introspection fields are free — they touch no metadata store.
	if len(f.Name) >= 2 && f.Name[:2] == "__" {
		return nil
	}
	w := a.weightFor(parentType, f.Name)
	mult := 1
	for _, argName := range w.Multipliers {
		if v := a.intArg(f.Args, argName, vars); v > 0 {
			if v > a.Limits.MaxFirst {
				v = a.Limits.MaxFirst
			}
			mult = v
			break
		}
	}
	if len(w.Multipliers) > 0 && a.intArg(f.Args, w.Multipliers[0], vars) <= 0 {
		mult = a.Limits.DefaultFirst
	}
	c.Complexity += w.Weight * mult

	childType := a.childType(parentType, f.Name)
	if len(f.Sels) > 0 {
		if err := a.walk(doc, f.Sels, childType, d+1, vars, visited, c); err != nil {
			return err
		}
	}
	return nil
}

func (a *Analyzer) weightFor(parent, field string) Weight {
	if t, ok := a.Weights.PerType[parent]; ok {
		if w, ok := t[field]; ok {
			return w
		}
	}
	return Weight{Weight: a.Weights.Default}
}

// childType returns the static return type name for parent.field. We hard-
// wire the named subset; unknown traversals fall back to a sentinel that
// short-circuits weight lookups (default weight = 1).
func (a *Analyzer) childType(parent, field string) string {
	if m, ok := childTypes[parent]; ok {
		if t, ok := m[field]; ok {
			return t
		}
	}
	return "_unknown_"
}

var childTypes = map[string]map[string]string{
	"Query": {
		"repository":   "Repository",
		"user":         "User",
		"agent":        "Agent",
		"pullRequest":  "PullRequest",
		"organization": "Organization",
	},
	"Repository": {
		"owner":        "User",
		"pullRequests": "PullRequestConnection",
		"issues":       "IssueConnection",
		"defaultBranch": "Ref",
	},
	"Organization": {
		"members": "UserConnection",
	},
	"PullRequestConnection": {
		"nodes":    "PullRequest",
		"pageInfo": "PageInfo",
	},
	"IssueConnection": {
		"nodes":    "Issue",
		"pageInfo": "PageInfo",
	},
	"UserConnection": {
		"nodes":    "User",
		"pageInfo": "PageInfo",
	},
	"PullRequest": {
		"author": "User",
	},
	"Mutation": {
		"createPullRequest":      "CreatePullRequestPayload",
		"createAgent":            "CreateAgentPayload",
		"updateAgentPermissions": "UpdateAgentPermissionsPayload",
	},
	"CreatePullRequestPayload":      {"pullRequest": "PullRequest"},
	"CreateAgentPayload":            {"agent": "Agent"},
	"UpdateAgentPermissionsPayload": {"agent": "Agent"},
}

// intArg returns the integer value of arg, resolving variable refs.
// Returns 0 when absent / non-integer.
func (a *Analyzer) intArg(args map[string]ArgValue, name string, vars map[string]any) int {
	v, ok := args[name]
	if !ok {
		return 0
	}
	if v.IntValue != nil {
		return *v.IntValue
	}
	if len(v.Raw) > 1 && v.Raw[0] == '$' {
		if vars == nil {
			return 0
		}
		got, ok := vars[v.Raw[1:]]
		if !ok {
			return 0
		}
		return coerceInt(got)
	}
	if n, err := strconv.Atoi(v.Raw); err == nil {
		return n
	}
	return 0
}

func coerceInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}
