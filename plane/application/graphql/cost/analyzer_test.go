package cost_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
)

func newAnalyzer() *cost.Analyzer {
	return cost.New(cost.DefaultLimits(), cost.DefaultFieldWeights())
}

func TestAnalyze_simpleLookup(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	c, err := a.Analyze(`{ user(login: "alice") { id name } }`, "", nil, cost.PrincipalHuman, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if c.Depth != 2 || c.Complexity < 1 {
		t.Fatalf("unexpected cost %+v", c)
	}
}

func TestAnalyze_connectionFirstMultiplier(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	c, err := a.Analyze(`{ repository(owner: "o", name: "r") { pullRequests(first: 50) { nodes { id } } } }`, "", nil, cost.PrincipalAgent, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// repository=1 + pullRequests=2*50=100 + nodes (default 1) + id (default 1)
	// = 103. We just assert >= 100 since default-weight contributions are
	// implementation-detail-stable but not contractually pinned here.
	if c.Complexity < 100 {
		t.Fatalf("expected ≥100 complexity, got %d", c.Complexity)
	}
}

func TestAnalyze_missingFirstFallsBackToDefault(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	c, err := a.Analyze(`{ repository(owner: "o", name: "r") { pullRequests { nodes { id } } } }`, "", nil, cost.PrincipalAgent, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// pullRequests fallback first=20 → 2*20=40 plus other weights.
	if c.Complexity < 40 || c.Complexity > 60 {
		t.Fatalf("expected ~40-60 complexity, got %d", c.Complexity)
	}
}

func TestAnalyze_firstCappedAtMaxFirst(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	c, err := a.Analyze(`{ repository(owner: "o", name: "r") { pullRequests(first: 9999) { nodes { id } } } }`, "", nil, cost.PrincipalAgent, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// pullRequests cap=100 → 2*100=200 (+ small constant). Reject only by depth/complexity max not yet.
	if c.Complexity < 200 || c.Complexity > 220 {
		t.Fatalf("expected ~200-220 complexity, got %d", c.Complexity)
	}
}

func TestAnalyze_depthExceededHuman(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	q := buildDeep(11)
	_, err := a.Analyze(q, "", nil, cost.PrincipalHuman, false)
	if !errors.Is(err, cost.ErrDepthExceeded) {
		t.Fatalf("want ErrDepthExceeded, got %v", err)
	}
}

func TestAnalyze_depthExceededAgentTighter(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	q := buildDeep(9)
	_, err := a.Analyze(q, "", nil, cost.PrincipalAgent, false)
	if !errors.Is(err, cost.ErrDepthExceeded) {
		t.Fatalf("want ErrDepthExceeded for agent at depth 9, got %v", err)
	}
}

func TestAnalyze_complexityBudgetExceeded(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	// Many connection fields with first=100 each blow past human budget=1000.
	var sb strings.Builder
	sb.WriteString(`{ a:repository(owner:"o", name:"r"){ pullRequests(first:100){ nodes{ id } } } `)
	for i := 0; i < 10; i++ {
		sb.WriteString(` b` )
		sb.WriteString(string(rune('a' + i)))
		sb.WriteString(`:repository(owner:"o", name:"r"){ pullRequests(first:100){ nodes{ id } } } `)
	}
	sb.WriteString(`}`)
	_, err := a.Analyze(sb.String(), "", nil, cost.PrincipalHuman, false)
	if !errors.Is(err, cost.ErrCostBudgetExceeded) {
		t.Fatalf("want ErrCostBudgetExceeded, got %v", err)
	}
}

func TestAnalyze_persistedDiscountApplies(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	q := `{ repository(owner: "o", name: "r") { pullRequests(first: 100) { nodes { id } } } }`
	full, err := a.Analyze(q, "", nil, cost.PrincipalAgent, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	disc, err := a.Analyze(q, "", nil, cost.PrincipalAgent, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if disc.Complexity >= full.Complexity {
		t.Fatalf("persisted discount did not apply: full=%d disc=%d", full.Complexity, disc.Complexity)
	}
}

func TestAnalyze_introspectionIsFree(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	c, err := a.Analyze(`{ __schema { queryType { name } } }`, "", nil, cost.PrincipalHuman, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if c.Complexity != 0 {
		t.Fatalf("introspection cost = %d, want 0", c.Complexity)
	}
}

func TestAnalyze_variableMultiplier(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	q := `query Q($n: Int!){ repository(owner:"o", name:"r"){ pullRequests(first: $n){ nodes { id } } } }`
	c, err := a.Analyze(q, "Q", map[string]any{"n": 50}, cost.PrincipalAgent, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if c.Complexity < 100 {
		t.Fatalf("variable not applied; complexity=%d", c.Complexity)
	}
}

func TestAnalyze_mutationOnRoot(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	q := `mutation { createAgent(input: {parentUserId:"u", displayName:"a", permissionScope:["x"]}) { agent { id } } }`
	c, err := a.Analyze(q, "", nil, cost.PrincipalHuman, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if c.Complexity < 10 {
		t.Fatalf("mutation cost = %d, want ≥10", c.Complexity)
	}
}

func TestAnalyze_parseError(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	_, err := a.Analyze(`{ broken `, "", nil, cost.PrincipalHuman, false)
	if !errors.Is(err, cost.ErrParse) {
		t.Fatalf("want ErrParse, got %v", err)
	}
}

func TestAnalyze_subscriptionRejected(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	_, err := a.Analyze(`subscription { x }`, "", nil, cost.PrincipalHuman, false)
	if !errors.Is(err, cost.ErrParse) {
		t.Fatalf("want ErrParse, got %v", err)
	}
}

func TestAnalyze_fragmentSpread(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	q := `query Q { user(login:"a") { ...UF } }
fragment UF on User { id name email }`
	c, err := a.Analyze(q, "Q", nil, cost.PrincipalHuman, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if c.Depth < 2 {
		t.Fatalf("expected depth ≥2 with fragment, got %d", c.Depth)
	}
}

func TestAnalyze_fragmentCycleRejected(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	q := `query Q { user(login:"a") { ...A } }
fragment A on User { ...B id }
fragment B on User { ...A name }`
	_, err := a.Analyze(q, "Q", nil, cost.PrincipalHuman, false)
	if !errors.Is(err, cost.ErrFragmentCycle) {
		t.Fatalf("want ErrFragmentCycle, got %v", err)
	}
}

func TestAnalyze_unknownOperationName(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	q := `query A { user(login:"a") { id } }
query B { user(login:"b") { id } }`
	_, err := a.Analyze(q, "Z", nil, cost.PrincipalHuman, false)
	if !errors.Is(err, cost.ErrUnknownOperation) {
		t.Fatalf("want ErrUnknownOperation, got %v", err)
	}
}

func TestAnalyze_ambiguousMultiOpRequiresName(t *testing.T) {
	t.Parallel()
	a := newAnalyzer()
	q := `query A { user(login:"a") { id } }
query B { user(login:"b") { id } }`
	_, err := a.Analyze(q, "", nil, cost.PrincipalHuman, false)
	if !errors.Is(err, cost.ErrAmbiguousOperation) {
		t.Fatalf("want ErrAmbiguousOperation, got %v", err)
	}
}

func TestParseCost_floor20(t *testing.T) {
	t.Parallel()
	if got := cost.ParseCost(cost.Cost{Complexity: 5}); got != 20 {
		t.Errorf("ParseCost low complexity got %d, want 20", got)
	}
	if got := cost.ParseCost(cost.Cost{Complexity: 1000}); got != 100 {
		t.Errorf("ParseCost 1000 got %d, want 100", got)
	}
}

// buildDeep returns a syntactically-valid query whose maximum nesting
// depth is exactly n. The cost analyser walks the AST without consulting
// the schema for unknown fields, so synthetic field names are sufficient
// to exercise the depth gate.
func buildDeep(n int) string {
	var sb strings.Builder
	sb.WriteString(`{ `)
	for i := 0; i < n; i++ {
		sb.WriteByte('a')
		if i < n-1 {
			sb.WriteString(` { `)
		}
	}
	for i := 0; i < n-1; i++ {
		sb.WriteString(` }`)
	}
	sb.WriteString(` }`)
	return sb.String()
}

// buildDeepLegacy is kept here only so the regression history of the deep-
// query trick is reviewable; it is intentionally unused.
//
//nolint:unused
func buildDeepLegacy(n int) string {
	// Build n nested user.author chains using the named subset:
	// `repository -> pullRequests -> nodes -> author -> ...`
	// We use repeated `repository` self-nesting via fragment trick? No —
	// resolver child types are fixed. Instead reach depth via
	// `repository{ pullRequests{ nodes{ author{ ... } } } }` chains with
	// inline fragments to pad depth where types don't naturally nest deep.
	var sb strings.Builder
	sb.WriteString(`{ repository(owner:"o", name:"r") {`)
	open := 1
	for i := 0; i < n-1; i++ {
		sb.WriteString(` ... on Repository { pullRequests(first:1) { nodes { author { ... on User {`)
		open++
	}
	sb.WriteString(` id `)
	for i := 0; i < open; i++ {
		sb.WriteString(`}`)
	}
	// Note: each "... on Repository { pullRequests { nodes { author { ... on User {"
	// added 5 levels per iteration. The simpler shape below is more
	// reliable: just chain inline fragments which the parser counts as
	// belonging to the same depth.
	return chainAuthor(n)
}

// chainAuthor is the historical depth-builder; superseded by buildDeep.
//
//nolint:unused
// It uses the natural Repository -> pullRequests -> nodes -> author -> ...
// chain and pads with inline fragments which DO count as depth-neutral
// containers (cost analyzer increments depth only on real fields).
//
// Pattern: repository(d=1).pullRequests(d=2).nodes(d=3).author(d=4).<padding-via-self-typed-inline>.id
// For depth >4 we re-enter PullRequestConnection by aliasing the same
// connection field again at depth=5+.
func chainAuthor(d int) string {
	if d < 2 {
		return `{ user(login:"a") { id } }`
	}
	// Build chain via repeated alias `repo: repository -> pullRequests -> nodes -> author -> ...`
	// Repository-only nesting via aliases:
	// { a:repository(...){ pullRequests(first:1){ nodes{ author{ id } } } } } = depth 5
	// To go deeper, wrap in alias selecting `pullRequest` again? Type Author
	// is User; from User we can go nowhere deep.
	// Use mutation fan-out alternative: wrap in Query → top-level alias
	// chain, each Repository selects a connection deeper.
	// Simpler shape: stack `nodes` -> `author` -> alias-back-to-repo via
	// fragment trick is impossible without that schema edge.
	// We adopt: build {a:user{id}} repeated under aliases at top level.
	// Top-level depth is 1; that doesn't grow depth.
	//
	// Final approach: nested Repository path:
	// repository -> pullRequests -> nodes -> author -> id  → depth 5.
	// Add alias siblings to grow depth via inline fragment-with-selection:
	var sb strings.Builder
	sb.WriteString(`{ repository(owner:"o", name:"r") `)
	depth := 1
	open := 0
	// Open d-1 nested levels using natural type chain repeatedly.
	for depth < d {
		sb.WriteString(` { pullRequests(first:1) { nodes { author `)
		depth += 4
		open += 4
		if depth >= d {
			break
		}
		// User has no further deep chain in our subset — pad with inline
		// fragment which preserves type and counts as depth-neutral.
	}
	sb.WriteString(` { id `)
	for i := 0; i < open+1; i++ {
		sb.WriteString(`}`)
	}
	sb.WriteString(`}`)
	return sb.String()
}
