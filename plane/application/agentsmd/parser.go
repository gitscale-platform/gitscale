package agentsmd

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse extracts a Policy and a list of Diagnostics from an AGENTS.md
// document. The parser is forgiving: malformed input yields diagnostics
// (severity error/warning) but does not return a non-nil error unless
// callers passed in something so corrupt that no policy could be
// constructed (e.g. structural YAML scanner errors). For all routine
// "the doc has problems" cases, callers should inspect Diagnostics.
//
// A nil/empty/whitespace-only input yields (&Policy{Empty: true}, nil, nil).
//
// Behaviour summary:
//   - Front-matter is delimited by "---" lines; the parser reads the
//     first delimited block as YAML.
//   - The YAML must contain a `schema:` field; only "gitscale/v1" is
//     accepted. Other values produce CodeUnsupportedSchemaVersion and the
//     returned policy is the empty policy (no enforcement).
//   - The Markdown body is scanned for a `## Never` heading. Bullet items
//     under that heading of the shape `- predicate_name: <selector>` are
//     parsed into NeverPredicate values.
//   - Unknown predicate names produce CodeUnknownPredicate; the predicate
//     is dropped (not enforced).
//   - Duplicate (name+selector) predicates produce CodeDuplicatePredicate;
//     the duplicate is dropped.
//   - An empty `## Never` block produces CodeEmptyNeverBlock (warning).
func Parse(data []byte) (*Policy, []Diagnostic, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return &Policy{Empty: true}, nil, nil
	}

	front, body, frontEndLine, frontDiag := splitFrontMatter(data)
	var diags []Diagnostic
	if frontDiag != nil {
		diags = append(diags, *frontDiag)
		// Without front-matter we cannot know the schema version. Treat as
		// empty to fail closed on enforcement.
		return &Policy{Empty: true}, diags, nil
	}

	var fm frontMatter
	if len(bytes.TrimSpace(front)) > 0 {
		if err := yaml.Unmarshal(front, &fm); err != nil {
			diags = append(diags, Diagnostic{
				Code:     CodeMalformedFrontMatter,
				Severity: SeverityError,
				Line:     1,
				Message:  fmt.Sprintf("front-matter YAML: %v", err),
			})
			return &Policy{Empty: true}, diags, nil
		}
	}

	if fm.Schema == "" {
		diags = append(diags, Diagnostic{
			Code:     CodeMalformedFrontMatter,
			Severity: SeverityError,
			Line:     1,
			Message:  "front-matter missing required `schema:` field",
		})
		return &Policy{Empty: true}, diags, nil
	}
	if fm.Schema != SchemaVersionV1 {
		diags = append(diags, Diagnostic{
			Code:     CodeUnsupportedSchemaVersion,
			Severity: SeverityError,
			Line:     1,
			Message:  fmt.Sprintf("unsupported schema version %q (expected %q)", fm.Schema, SchemaVersionV1),
		})
		return &Policy{Empty: true}, diags, nil
	}

	preds, predDiags := parseNeverBlock(body, frontEndLine)
	diags = append(diags, predDiags...)

	policy := &Policy{
		SchemaVersion: fm.Schema,
		Never:         preds,
		Empty:         len(preds) == 0,
	}
	return policy, diags, nil
}

// frontMatter mirrors the YAML keys we read from the document head. We
// keep the surface minimal; future fields are additive.
type frontMatter struct {
	Schema  string `yaml:"schema"`
	Version string `yaml:"version"`
}

// splitFrontMatter looks for a leading `---\n...\n---` block. Returns
// (frontBytes, bodyBytes, lineAfterFront, diag). If the input does not
// begin with `---`, the entire input is treated as body and an
// "missing front matter" diagnostic is returned.
func splitFrontMatter(data []byte) ([]byte, []byte, int, *Diagnostic) {
	lines := splitLines(data)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, data, 0, &Diagnostic{
			Code:     CodeMalformedFrontMatter,
			Severity: SeverityError,
			Line:     1,
			Message:  "AGENTS.md must begin with a `---` front-matter delimiter",
		}
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			front := bytes.Join(bytesFromLines(lines[1:i]), []byte("\n"))
			body := bytes.Join(bytesFromLines(lines[i+1:]), []byte("\n"))
			return front, body, i + 1, nil
		}
	}
	return nil, data, 0, &Diagnostic{
		Code:     CodeMalformedFrontMatter,
		Severity: SeverityError,
		Line:     1,
		Message:  "front-matter block opened with `---` but never closed",
	}
}

// splitLines returns the input split on '\n' without dropping trailing empties.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	return strings.Split(string(data), "\n")
}

func bytesFromLines(lines []string) [][]byte {
	out := make([][]byte, len(lines))
	for i, l := range lines {
		out[i] = []byte(l)
	}
	return out
}

// parseNeverBlock scans the Markdown body for a `## Never` heading and
// extracts bullet-item predicates beneath it (until the next heading at
// the same or higher level, or end of document).
func parseNeverBlock(body []byte, lineOffset int) ([]NeverPredicate, []Diagnostic) {
	lines := splitLines(body)
	var (
		preds      []NeverPredicate
		diags      []Diagnostic
		seen       = map[string]struct{}{}
		inNever    bool
		neverStart int
		neverCount int
	)
	known := knownPredicates()
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r\t ")
		fileLine := lineOffset + i + 1
		trim := strings.TrimSpace(line)

		// Headings change scan state.
		if strings.HasPrefix(trim, "#") {
			if inNever && neverCount == 0 {
				diags = append(diags, Diagnostic{
					Code:     CodeEmptyNeverBlock,
					Severity: SeverityWarning,
					Line:     neverStart,
					Message:  "`## Never` block contained no predicates",
				})
			}
			heading := strings.TrimSpace(strings.TrimLeft(trim, "#"))
			if strings.EqualFold(heading, "Never") {
				inNever = true
				neverStart = fileLine
				neverCount = 0
				continue
			}
			inNever = false
			// Other recognised headings: Always, Prefer (advisory only in
			// this iteration; presence is fine, contents not parsed).
			if !isKnownHeading(heading) {
				diags = append(diags, Diagnostic{
					Code:     CodeUnknownSection,
					Severity: SeverityWarning,
					Line:     fileLine,
					Message:  fmt.Sprintf("unknown section heading %q (advisory)", heading),
				})
			}
			continue
		}

		if !inNever {
			continue
		}

		if !strings.HasPrefix(trim, "- ") {
			continue
		}

		neverCount++
		item := strings.TrimSpace(strings.TrimPrefix(trim, "- "))
		pred, perr := parsePredicateItem(item)
		if perr != "" {
			diags = append(diags, Diagnostic{
				Code:     CodeMalformedPredicate,
				Severity: SeverityError,
				Line:     fileLine,
				Message:  perr,
			})
			continue
		}
		if _, ok := known[pred.Name]; !ok {
			diags = append(diags, Diagnostic{
				Code:     CodeUnknownPredicate,
				Severity: SeverityError,
				Line:     fileLine,
				Message:  fmt.Sprintf("unknown predicate %q (skipped, not enforced)", pred.Name),
			})
			continue
		}
		k := pred.key()
		if _, dup := seen[k]; dup {
			diags = append(diags, Diagnostic{
				Code:     CodeDuplicatePredicate,
				Severity: SeverityWarning,
				Line:     fileLine,
				Message:  fmt.Sprintf("duplicate predicate %q ignored", pred.Name),
			})
			continue
		}
		seen[k] = struct{}{}
		preds = append(preds, pred)
	}
	if inNever && neverCount == 0 {
		diags = append(diags, Diagnostic{
			Code:     CodeEmptyNeverBlock,
			Severity: SeverityWarning,
			Line:     neverStart,
			Message:  "`## Never` block contained no predicates",
		})
	}
	return preds, diags
}

func knownPredicates() map[PredicateName]struct{} {
	m := map[PredicateName]struct{}{}
	for _, p := range AllPredicates() {
		m[p] = struct{}{}
	}
	return m
}

func isKnownHeading(h string) bool {
	switch strings.ToLower(h) {
	case "never", "always", "prefer":
		return true
	}
	return false
}

// parsePredicateItem parses a single bullet item body of the form
// `predicate_name: selector`. The selector grammar is predicate-specific:
//
//   - force_push_to_branch / delete_branch / push_to_branch:
//     the selector is a branch glob (e.g. "main", "release/*").
//   - modify_path: the selector is a path glob (e.g. "infra/**").
//   - push_binary_over_size: the selector is an int64 byte size, optionally
//     suffixed K/M/G (case-insensitive, base 1024).
//
// On error returns a non-empty message; the caller emits the diagnostic.
func parsePredicateItem(item string) (NeverPredicate, string) {
	colon := strings.Index(item, ":")
	if colon < 0 {
		return NeverPredicate{}, fmt.Sprintf("malformed predicate %q: expected `name: selector`", item)
	}
	name := PredicateName(strings.TrimSpace(item[:colon]))
	selector := strings.TrimSpace(item[colon+1:])
	if name == "" || selector == "" {
		return NeverPredicate{}, fmt.Sprintf("malformed predicate %q: name or selector empty", item)
	}
	pred := NeverPredicate{Name: name}
	switch name {
	case PredicateForcePushToBranch, PredicateDeleteBranch, PredicatePushToBranch:
		pred.Selector.BranchGlob = unquote(selector)
	case PredicateModifyPath:
		pred.Selector.PathGlob = unquote(selector)
	case PredicatePushBinaryOverSize:
		n, err := parseSize(unquote(selector))
		if err != nil {
			return NeverPredicate{}, fmt.Sprintf("malformed size %q: %v", selector, err)
		}
		pred.Selector.MaxBytes = n
	default:
		// Unknown predicate name; carry the value through so the caller
		// can emit CodeUnknownPredicate (which is more specific than
		// "malformed").
		pred.Selector.BranchGlob = unquote(selector)
	}
	return pred, ""
}

func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func parseSize(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mul := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		mul = 1024
		s = s[:len(s)-1]
	case 'm', 'M':
		mul = 1024 * 1024
		s = s[:len(s)-1]
	case 'g', 'G':
		mul = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit %q", c)
		}
		n = n*10 + int64(c-'0')
	}
	return n * mul, nil
}
