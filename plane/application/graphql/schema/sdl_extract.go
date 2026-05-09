package schema

import (
	"bufio"
	"strings"
)

// extractTypeFields returns a map of object-type-name → set of field names
// from a GraphQL SDL string. Comments (#…), descriptions ("…"), directives,
// arguments, and types are stripped — only top-level field names per
// `type X { … }` block are recorded.
//
// The parser is intentionally minimal: it is sufficient for the compat test
// (named-subset diff against the GitHub snapshot) and the deprecated-field
// lint, both of which only need field names.
func extractTypeFields(sdl string) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{})
	scan := bufio.NewScanner(strings.NewReader(sdl))
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentType string
	var braceDepth int

	for scan.Scan() {
		line := stripComment(scan.Text())
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Type header. We only care about `type X` blocks (object types);
		// `input`, `enum`, `scalar`, `directive`, `schema`, `union`,
		// `interface` are skipped.
		if braceDepth == 0 && strings.HasPrefix(trimmed, "type ") {
			name := strings.Fields(trimmed)[1]
			// Strip directives glued to the type name, e.g. `Agent @x`.
			if idx := strings.IndexAny(name, " \t"); idx >= 0 {
				name = name[:idx]
			}
			currentType = name
			if _, ok := out[currentType]; !ok {
				out[currentType] = make(map[string]struct{})
			}
		}

		// Brace tracking — handles same-line `{ … }` and multi-line blocks.
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")
		braceDepth += opens - closes

		if currentType != "" && braceDepth > 0 {
			// Field line inside a type block. A field starts with an
			// identifier and is followed by `:` (after optional argument
			// list).
			if name, ok := extractFieldName(trimmed); ok {
				out[currentType][name] = struct{}{}
			}
		}

		if braceDepth <= 0 {
			currentType = ""
			braceDepth = 0
		}
	}
	return out
}

// stripComment trims a line at the first un-quoted `#`. SDL only quotes
// strings with `"` and `"""`; for our minimal parser we accept the
// approximation that `#` outside a quoted run starts a comment. The
// schema.graphql file does not embed `#` inside descriptions; if that ever
// changes the test suite will catch it (compat test parses both files).
func stripComment(s string) string {
	inStr := false
	for i, r := range s {
		if r == '"' {
			inStr = !inStr
		}
		if r == '#' && !inStr {
			return s[:i]
		}
	}
	return s
}

// extractFieldName returns the leading identifier of a field-definition
// line and true on success. Lines that contain only `{`, `}`, or directive
// applications are rejected.
func extractFieldName(s string) (string, bool) {
	if s == "" || s[0] == '{' || s[0] == '}' || s[0] == '"' || s[0] == '@' {
		return "", false
	}
	// First identifier run.
	end := 0
	for end < len(s) {
		c := s[end]
		if isIdent(c) {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return "", false
	}
	name := s[:end]
	// Reserved keywords.
	switch name {
	case "type", "enum", "scalar", "input", "interface", "union",
		"directive", "schema", "extend", "implements":
		return "", false
	}
	// Field must be followed by `:` (no args) or `(` (args) eventually
	// followed by `:`.
	rest := strings.TrimSpace(s[end:])
	if rest == "" {
		return "", false
	}
	if rest[0] != ':' && rest[0] != '(' {
		return "", false
	}
	return name, true
}

func isIdent(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}
