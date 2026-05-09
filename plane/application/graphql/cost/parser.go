// Package cost implements pre-execution depth + complexity analysis for
// GraphQL queries, the load-bearing DoS guard documented in the spec.
//
// The cost analyzer parses the query string with an in-package tokeniser,
// not the upstream GraphQL library's internal AST, so we are insulated from
// upstream churn (the library's AST types live under internal/). The
// tokeniser is deliberately small: it understands operations, named
// fragments, fields, integer/string/identifier arguments, and nested
// selection sets — the full surface needed for depth + complexity scoring
// — and rejects anything else as a parse error.
//
// Rejection is structurally typed via ErrDepthExceeded / ErrCostBudget so
// the router can map to GraphQL extensions.code without a string match.
package cost

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// OperationKind discriminates query vs mutation. Subscriptions are
// out-of-scope per spec.
type OperationKind int

const (
	OpQuery OperationKind = iota
	OpMutation
)

// Document is the parsed query AST minimal enough for cost analysis.
type Document struct {
	Operations []Operation
	Fragments  map[string]Fragment
}

// Operation is a top-level operation definition.
type Operation struct {
	Kind OperationKind
	Name string
	Sels []Selection
}

// Fragment is a named fragment definition (`fragment X on T { ... }`).
type Fragment struct {
	Name      string
	OnType    string
	Sels      []Selection
}

// Selection is either a Field, an InlineFragment, or a FragmentSpread.
type Selection struct {
	Kind     SelectionKind
	Field    Field
	Fragment FragmentRef
}

// SelectionKind discriminates Selection.
type SelectionKind int

const (
	SelField SelectionKind = iota
	SelFragmentSpread
	SelInlineFragment
)

// Field is a field selection.
type Field struct {
	Name       string
	Alias      string
	Args       map[string]ArgValue
	Directives []string // bare directive names (no args parsed for cost)
	Sels       []Selection
}

// FragmentRef is either a spread `...Name` or an inline `... on T { ... }`.
type FragmentRef struct {
	Name   string      // empty for inline
	OnType string      // populated for inline
	Sels   []Selection // populated for inline
}

// ArgValue is a minimal argument-value shape. Cost analysis only needs
// integer pagination args (`first` / `last`) and recognising scalar
// presence; we therefore do not model object/list values precisely.
type ArgValue struct {
	IntValue *int
	Raw      string
}

// ErrParse signals a malformed query body.
var ErrParse = errors.New("cost: parse error")

// ParseQuery tokenises and parses a GraphQL operation document. The result
// is enough to compute depth + complexity. Variable definitions are
// accepted but their default values are not parsed beyond skipping; the
// cost analyser substitutes runtime variable values for connection
// multipliers via Analyzer.Analyze's vars argument.
func ParseQuery(src string) (*Document, error) {
	p := &parser{src: src}
	p.skipWS()
	doc := &Document{Fragments: map[string]Fragment{}}

	// A bare selection set is sugar for an unnamed query.
	if p.peek() == '{' {
		sels, err := p.parseSelectionSet()
		if err != nil {
			return nil, err
		}
		doc.Operations = append(doc.Operations, Operation{Kind: OpQuery, Sels: sels})
		p.skipWS()
		if !p.eof() {
			return nil, p.errf("trailing input after shorthand operation")
		}
		return doc, nil
	}

	for !p.eof() {
		kw := p.readIdent()
		switch kw {
		case "query", "mutation":
			op, err := p.parseOperation(kindFor(kw))
			if err != nil {
				return nil, err
			}
			doc.Operations = append(doc.Operations, op)
		case "subscription":
			return nil, p.errf("subscriptions are not supported")
		case "fragment":
			frag, err := p.parseFragment()
			if err != nil {
				return nil, err
			}
			doc.Fragments[frag.Name] = frag
		case "":
			return nil, p.errf("expected operation or fragment")
		default:
			return nil, p.errf("unexpected top-level token %q", kw)
		}
		p.skipWS()
	}
	if len(doc.Operations) == 0 {
		return nil, fmt.Errorf("%w: document has no operations", ErrParse)
	}
	return doc, nil
}

func kindFor(kw string) OperationKind {
	if kw == "mutation" {
		return OpMutation
	}
	return OpQuery
}

// parser is the recursive-descent state.
type parser struct {
	src string
	pos int
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) skipWS() {
	for !p.eof() {
		c := p.src[p.pos]
		switch c {
		case ' ', '\t', '\n', '\r', ',':
			p.pos++
		case '#':
			for !p.eof() && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *parser) readIdent() string {
	p.skipWS()
	start := p.pos
	for !p.eof() {
		c := p.src[p.pos]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			p.pos++
			continue
		}
		break
	}
	return p.src[start:p.pos]
}

func (p *parser) errf(format string, args ...any) error {
	loc := fmt.Sprintf(" at offset %d", p.pos)
	return fmt.Errorf("%w: "+format+loc, append([]any{ErrParse}, args...)...)
}

func (p *parser) expect(c byte) error {
	p.skipWS()
	if p.eof() || p.src[p.pos] != c {
		return p.errf("expected %q", c)
	}
	p.pos++
	return nil
}

func (p *parser) parseOperation(kind OperationKind) (Operation, error) {
	op := Operation{Kind: kind}
	op.Name = p.readIdent()
	p.skipWS()
	// Variable definitions: `( $x: T = default, ... )`.
	if p.peek() == '(' {
		if err := p.skipBalanced('(', ')'); err != nil {
			return op, err
		}
	}
	p.skipWS()
	// Operation-level directives.
	for p.peek() == '@' {
		p.pos++ // consume `@`
		_ = p.readIdent()
		p.skipWS()
		if p.peek() == '(' {
			if err := p.skipBalanced('(', ')'); err != nil {
				return op, err
			}
		}
		p.skipWS()
	}
	sels, err := p.parseSelectionSet()
	if err != nil {
		return op, err
	}
	op.Sels = sels
	return op, nil
}

func (p *parser) parseFragment() (Fragment, error) {
	frag := Fragment{}
	frag.Name = p.readIdent()
	if frag.Name == "" {
		return frag, p.errf("fragment requires a name")
	}
	on := p.readIdent()
	if on != "on" {
		return frag, p.errf("expected 'on' after fragment name")
	}
	frag.OnType = p.readIdent()
	if frag.OnType == "" {
		return frag, p.errf("fragment requires a type condition")
	}
	sels, err := p.parseSelectionSet()
	if err != nil {
		return frag, err
	}
	frag.Sels = sels
	return frag, nil
}

func (p *parser) parseSelectionSet() ([]Selection, error) {
	if err := p.expect('{'); err != nil {
		return nil, err
	}
	var out []Selection
	for {
		p.skipWS()
		if p.eof() {
			return nil, p.errf("unclosed selection set")
		}
		if p.peek() == '}' {
			p.pos++
			return out, nil
		}
		// Fragment spread / inline fragment.
		if p.pos+2 < len(p.src) && p.src[p.pos] == '.' && p.src[p.pos+1] == '.' && p.src[p.pos+2] == '.' {
			p.pos += 3
			p.skipWS()
			ident := p.readIdent()
			if ident == "on" {
				// Inline fragment.
				typeName := p.readIdent()
				p.skipWS()
				// Skip directives.
				for p.peek() == '@' {
					p.pos++
					_ = p.readIdent()
					p.skipWS()
					if p.peek() == '(' {
						if err := p.skipBalanced('(', ')'); err != nil {
							return nil, err
						}
					}
					p.skipWS()
				}
				sels, err := p.parseSelectionSet()
				if err != nil {
					return nil, err
				}
				out = append(out, Selection{
					Kind:     SelInlineFragment,
					Fragment: FragmentRef{OnType: typeName, Sels: sels},
				})
				continue
			}
			// Spread.
			out = append(out, Selection{
				Kind:     SelFragmentSpread,
				Fragment: FragmentRef{Name: ident},
			})
			continue
		}
		f, err := p.parseField()
		if err != nil {
			return nil, err
		}
		out = append(out, Selection{Kind: SelField, Field: f})
	}
}

func (p *parser) parseField() (Field, error) {
	f := Field{Args: map[string]ArgValue{}}
	name := p.readIdent()
	if name == "" {
		return f, p.errf("expected field name")
	}
	p.skipWS()
	if p.peek() == ':' {
		p.pos++
		p.skipWS()
		f.Alias = name
		name = p.readIdent()
		if name == "" {
			return f, p.errf("expected field name after alias")
		}
		p.skipWS()
	}
	f.Name = name

	if p.peek() == '(' {
		if err := p.parseArgs(f.Args); err != nil {
			return f, err
		}
	}
	p.skipWS()
	for p.peek() == '@' {
		p.pos++
		dname := p.readIdent()
		f.Directives = append(f.Directives, dname)
		p.skipWS()
		if p.peek() == '(' {
			if err := p.skipBalanced('(', ')'); err != nil {
				return f, err
			}
		}
		p.skipWS()
	}
	if p.peek() == '{' {
		sels, err := p.parseSelectionSet()
		if err != nil {
			return f, err
		}
		f.Sels = sels
	}
	return f, nil
}

func (p *parser) parseArgs(into map[string]ArgValue) error {
	if err := p.expect('('); err != nil {
		return err
	}
	for {
		p.skipWS()
		if p.eof() {
			return p.errf("unclosed args")
		}
		if p.peek() == ')' {
			p.pos++
			return nil
		}
		name := p.readIdent()
		if name == "" {
			return p.errf("expected arg name")
		}
		p.skipWS()
		if err := p.expect(':'); err != nil {
			return err
		}
		p.skipWS()
		val, err := p.parseValue()
		if err != nil {
			return err
		}
		into[name] = val
	}
}

func (p *parser) parseValue() (ArgValue, error) {
	p.skipWS()
	if p.eof() {
		return ArgValue{}, p.errf("expected value")
	}
	c := p.src[p.pos]
	switch {
	case c == '$':
		// Variable reference.
		p.pos++
		name := p.readIdent()
		return ArgValue{Raw: "$" + name}, nil
	case c == '"':
		// String (incl. block string """).
		raw, err := p.readString()
		if err != nil {
			return ArgValue{}, err
		}
		return ArgValue{Raw: raw}, nil
	case c == '[':
		// List — capture braced span; not analysed for cost.
		start := p.pos
		if err := p.skipBalanced('[', ']'); err != nil {
			return ArgValue{}, err
		}
		return ArgValue{Raw: p.src[start:p.pos]}, nil
	case c == '{':
		start := p.pos
		if err := p.skipBalanced('{', '}'); err != nil {
			return ArgValue{}, err
		}
		return ArgValue{Raw: p.src[start:p.pos]}, nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.readNumber()
	default:
		// Boolean, null, enum value — treated as raw identifiers.
		ident := p.readIdent()
		if ident == "" {
			return ArgValue{}, p.errf("unexpected character %q in value", c)
		}
		return ArgValue{Raw: ident}, nil
	}
}

func (p *parser) readNumber() (ArgValue, error) {
	start := p.pos
	if p.src[p.pos] == '-' {
		p.pos++
	}
	for !p.eof() {
		c := p.src[p.pos]
		if c >= '0' && c <= '9' {
			p.pos++
			continue
		}
		break
	}
	hasDot := false
	if !p.eof() && p.src[p.pos] == '.' {
		hasDot = true
		p.pos++
		for !p.eof() {
			c := p.src[p.pos]
			if c >= '0' && c <= '9' {
				p.pos++
				continue
			}
			break
		}
	}
	raw := p.src[start:p.pos]
	if hasDot {
		return ArgValue{Raw: raw}, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return ArgValue{Raw: raw}, nil
	}
	return ArgValue{Raw: raw, IntValue: &v}, nil
}

func (p *parser) readString() (string, error) {
	if strings.HasPrefix(p.src[p.pos:], `"""`) {
		end := strings.Index(p.src[p.pos+3:], `"""`)
		if end < 0 {
			return "", p.errf("unterminated block string")
		}
		raw := p.src[p.pos : p.pos+3+end+3]
		p.pos += 3 + end + 3
		return raw, nil
	}
	start := p.pos
	p.pos++ // opening "
	for !p.eof() {
		c := p.src[p.pos]
		if c == '\\' {
			p.pos += 2
			continue
		}
		if c == '"' {
			p.pos++
			return p.src[start:p.pos], nil
		}
		p.pos++
	}
	return "", p.errf("unterminated string")
}

func (p *parser) skipBalanced(open, close byte) error {
	if p.peek() != open {
		return p.errf("expected %q", open)
	}
	depth := 0
	for !p.eof() {
		c := p.src[p.pos]
		switch c {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				p.pos++
				return nil
			}
		case '"':
			if _, err := p.readString(); err != nil {
				return err
			}
			continue
		}
		p.pos++
	}
	return p.errf("unbalanced %q…%q", open, close)
}
