package agentsmd

// Lint returns only the diagnostics from Parse. It is the convenience
// wrapper used by the MCP `agents_md_lint` tool (#112) — humans and IDEs
// don't usually need the parsed Policy, just the structured findings.
//
// A clean document returns nil; an unparseable document returns the
// diagnostics that would have been produced by Parse.
func Lint(data []byte) []Diagnostic {
	_, diags, _ := Parse(data)
	return diags
}
