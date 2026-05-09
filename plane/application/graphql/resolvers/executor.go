package resolvers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
)

// Executor walks a parsed cost.Operation and emits a JSON result tree.
// Field dispatch is hand-coded against the named subset; unknown fields
// return a structured FIELD_NOT_SUPPORTED-class error.
//
// The executor is deliberately tiny: it owes its existence to the
// observation that the GitScale GraphQL surface is a fixed, versioned
// contract, not a general-purpose graph. A library that does runtime
// reflection-based field binding adds complexity without buying us
// flexibility; an explicit dispatch is reviewable, stable, and faster.
type Executor struct {
	Deps Deps
	Vars map[string]any
}

// Result is the executor's output. `Data` is the field tree (nil on
// fatal-class errors); `Errors` is a path-tagged list of field-level errors.
type Result struct {
	Data   map[string]any  `json:"data"`
	Errors []ExecutorError `json:"errors,omitempty"`
}

// ExecutorError is the field-level error envelope. Translated to the
// GraphQL public Error shape by the router.
type ExecutorError struct {
	Message string
	Path    []any
	Cause   error // sentinel used by router's errors.MapErr
}

// Execute runs op against e.Deps and returns the materialised tree.
func (e *Executor) Execute(ctx context.Context, doc *cost.Document, op cost.Operation) Result {
	rootName := "Query"
	if op.Kind == cost.OpMutation {
		rootName = "Mutation"
	}
	res := Result{Data: map[string]any{}}
	for _, sel := range op.Sels {
		switch sel.Kind {
		case cost.SelField:
			val, err := e.dispatchRoot(ctx, rootName, sel.Field, doc)
			out := keyOf(sel.Field)
			if err != nil {
				res.Errors = append(res.Errors, ExecutorError{
					Message: err.Error(),
					Path:    []any{out},
					Cause:   err,
				})
				res.Data[out] = nil
				continue
			}
			res.Data[out] = val
		case cost.SelInlineFragment, cost.SelFragmentSpread:
			// Fragments at the root are flattened by the executor below
			// once we descend; root-level fragments are unusual but
			// permitted.
			expanded := expandFragment(doc, sel)
			for _, child := range expanded {
				val, err := e.dispatchRoot(ctx, rootName, child, doc)
				out := keyOf(child)
				if err != nil {
					res.Errors = append(res.Errors, ExecutorError{
						Message: err.Error(),
						Path:    []any{out},
						Cause:   err,
					})
					res.Data[out] = nil
					continue
				}
				res.Data[out] = val
			}
		}
	}
	return res
}

func keyOf(f cost.Field) string {
	if f.Alias != "" {
		return f.Alias
	}
	return f.Name
}

// dispatchRoot picks the resolver for a top-level field. Introspection
// (`__schema`, `__type`, `__typename`) is handled inline so it works
// without authentication and at zero metadata cost.
func (e *Executor) dispatchRoot(ctx context.Context, root string, f cost.Field, doc *cost.Document) (any, error) {
	if f.Name == "__schema" {
		return resolveSchemaIntrospection(f, doc), nil
	}
	if f.Name == "__type" {
		return resolveTypeIntrospection(f), nil
	}
	if f.Name == "__typename" {
		return root, nil
	}
	if root == "Query" {
		return e.resolveQueryRoot(ctx, f, doc)
	}
	return e.resolveMutationRoot(ctx, f, doc)
}

// argString returns the string value of arg, resolving variable refs.
func (e *Executor) argString(args map[string]cost.ArgValue, name string) (string, bool) {
	v, ok := args[name]
	if !ok {
		return "", false
	}
	if len(v.Raw) > 1 && v.Raw[0] == '$' {
		got, ok := e.Vars[v.Raw[1:]]
		if !ok {
			return "", false
		}
		s, ok := got.(string)
		return s, ok
	}
	// Strip surrounding quotes if a quoted string literal.
	raw := v.Raw
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			return s, true
		}
	}
	return raw, true
}

// argInputObject returns the variable-substituted object value of arg as a
// map[string]any. Inline object literals are JSON-decoded after a small
// transform that converts unquoted GraphQL keys to JSON keys.
func (e *Executor) argInputObject(args map[string]cost.ArgValue, name string) (map[string]any, error) {
	v, ok := args[name]
	if !ok {
		return nil, fmt.Errorf("missing arg %q", name)
	}
	if len(v.Raw) > 1 && v.Raw[0] == '$' {
		got, ok := e.Vars[v.Raw[1:]]
		if !ok {
			return nil, fmt.Errorf("variable %s not bound", v.Raw)
		}
		if m, ok := got.(map[string]any); ok {
			return m, nil
		}
		// Re-marshal/unmarshal for type-erasure safety.
		b, err := json.Marshal(got)
		if err != nil {
			return nil, err
		}
		out := map[string]any{}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	jsonish, err := graphqlObjectToJSON(v.Raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(jsonish), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// graphqlObjectToJSON converts a GraphQL object literal (`{key: "v", n: 1}`)
// into a JSON object string (`{"key":"v","n":1}`). It handles nested
// objects and lists; enum values are quoted as strings.
func graphqlObjectToJSON(s string) (string, error) {
	out := make([]byte, 0, len(s)+16)
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\n' || c == '\t' || c == '\r':
			out = append(out, ' ')
			i++
		case c == ',':
			// GraphQL treats commas and whitespace as equivalent token
			// separators. JSON requires explicit commas between pairs;
			// since we always emit one comma per separator the encoded
			// stream is well-formed.
			out = append(out, ',')
			i++
		case c == '{' || c == '}' || c == '[' || c == ']' || c == ':':
			out = append(out, c)
			i++
		case c == '"':
			// Quoted string — copy verbatim including escapes.
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' {
					j += 2
					continue
				}
				if s[j] == '"' {
					j++
					break
				}
				j++
			}
			out = append(out, s[i:j]...)
			i = j
		case (c >= '0' && c <= '9') || c == '-' || c == '.':
			j := i
			for j < len(s) && (s[j] == '-' || s[j] == '.' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			out = append(out, s[i:j]...)
			i = j
		case isLetter(c):
			j := i
			for j < len(s) && (isLetter(s[j]) || (s[j] >= '0' && s[j] <= '9') || s[j] == '_') {
				j++
			}
			ident := s[i:j]
			// Distinguish object-key (followed by `:`) from value (true,
			// false, null, enum).
			k := j
			for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
				k++
			}
			if k < len(s) && s[k] == ':' {
				out = append(out, '"')
				out = append(out, ident...)
				out = append(out, '"')
			} else {
				switch ident {
				case "true", "false", "null":
					out = append(out, ident...)
				default:
					// Enum value → JSON string.
					out = append(out, '"')
					out = append(out, ident...)
					out = append(out, '"')
				}
			}
			i = j
		default:
			return "", fmt.Errorf("unexpected character %q in object literal", c)
		}
	}
	return string(out), nil
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

// expandFragment returns the field selections from an inline-fragment or
// named spread. Unknown fragment names are silently dropped (the analyzer
// has already validated the document; a missing fragment here is a
// programming error and is logged elsewhere).
func expandFragment(doc *cost.Document, sel cost.Selection) []cost.Field {
	var sels []cost.Selection
	switch sel.Kind {
	case cost.SelInlineFragment:
		sels = sel.Fragment.Sels
	case cost.SelFragmentSpread:
		if frag, ok := doc.Fragments[sel.Fragment.Name]; ok {
			sels = frag.Sels
		}
	}
	out := []cost.Field{}
	for _, s := range sels {
		if s.Kind == cost.SelField {
			out = append(out, s.Field)
		}
	}
	return out
}

// formatInt is exposed to resolver helpers that need to render ints into
// JSON values; kept for symmetry with future connection-type cursors.
//
//nolint:unused // retained as part of the executor's stable helper surface.
func formatInt(n int) string { return strconv.Itoa(n) }
