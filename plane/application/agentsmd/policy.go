package agentsmd

// SchemaVersionV1 is the only schema version accepted in this iteration.
// See doc.go for the schema-versioning open arch question.
const SchemaVersionV1 = "gitscale/v1"

// Policy is the parsed, validated representation of an AGENTS.md document.
// An "empty" policy (Empty == true) carries no predicates and matches no
// updates; it is the result of parsing nil/whitespace-only input.
type Policy struct {
	SchemaVersion string
	Empty         bool
	Never         []NeverPredicate
}

// PredicateName is the closed enumeration of v1 Never-predicate names.
// Unknown names are surfaced as a CodeUnknownPredicate diagnostic at parse
// time and are not enforced (see spec).
type PredicateName string

const (
	PredicateForcePushToBranch  PredicateName = "force_push_to_branch"
	PredicateDeleteBranch       PredicateName = "delete_branch"
	PredicatePushToBranch       PredicateName = "push_to_branch"
	PredicateModifyPath         PredicateName = "modify_path"
	PredicatePushBinaryOverSize PredicateName = "push_binary_over_size"
)

// AllPredicates is exposed for tests and tools (e.g. MCP) that enumerate
// the closed v1 vocabulary.
func AllPredicates() []PredicateName {
	return []PredicateName{
		PredicateForcePushToBranch,
		PredicateDeleteBranch,
		PredicatePushToBranch,
		PredicateModifyPath,
		PredicatePushBinaryOverSize,
	}
}

// PredicateSelector carries the typed inputs each predicate consumes.
// Only the field(s) relevant to a given predicate are populated; the
// evaluator selects on Name and ignores irrelevant fields.
type PredicateSelector struct {
	BranchGlob string // *_branch predicates
	PathGlob   string // modify_path
	MaxBytes   int64  // push_binary_over_size
}

// NeverPredicate is a single rule under the `## Never` Markdown heading.
type NeverPredicate struct {
	Name     PredicateName
	Selector PredicateSelector
}

// key uniquely identifies a predicate for dedup/merge purposes. Name +
// every selector field — two predicates collide only if both name and
// selector match exactly.
func (p NeverPredicate) key() string {
	return string(p.Name) + "|" + p.Selector.BranchGlob + "|" + p.Selector.PathGlob + "|" + itoa64(p.Selector.MaxBytes)
}

func itoa64(n int64) string {
	// stdlib strconv would import a bigger surface; tiny helper keeps
	// this file dependency-free.
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
