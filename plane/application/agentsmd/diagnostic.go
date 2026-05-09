package agentsmd

// Severity is the closed enumeration of diagnostic severities surfaced by
// Parse / Lint. Only "error" and "warning" are emitted; callers may treat
// "error" as blocking for editor surfaces (the pre-receive hook does not
// block on parse diagnostics — see the spec error-handling table).
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Stable diagnostic codes. New codes may be added in additive minor
// releases; existing codes will not be renamed. The MCP `agents_md_lint`
// tool surfaces these to humans and IDEs.
const (
	CodeUnknownSection            = "unknown_section"
	CodeMalformedPredicate        = "malformed_predicate"
	CodeUnsupportedSchemaVersion  = "unsupported_schema_version"
	CodeDuplicatePredicate        = "duplicate_predicate"
	CodeEmptyNeverBlock           = "empty_never_block"
	CodeUnknownPredicate          = "unknown_predicate"
	CodeMalformedFrontMatter      = "malformed_front_matter"
)

// Diagnostic is a single parser/linter finding. Line is 1-based; 0 means
// "no line context available" (e.g. front-matter-level errors).
type Diagnostic struct {
	Code     string
	Severity Severity
	Line     int
	Message  string
}
