// Package contractdsl is the closed grammar for behavior-contract statements.
//
// A statement is one call in Python call syntax with literal arguments:
//
//	tool_cardinality("issue_refund", per="order_id", at_most=1)
//
// A hand-written lexer and recursive-descent parser read it; nothing is
// evaluated. Only literals (strings, numbers, booleans, null, lists and
// string-keyed maps) are values, and only methods in the catalog are callable.
package contractdsl

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits on one statement and one call to the server.
const (
	MaxStatementBytes = 8192
	MaxDepth          = 8
	MaxStatements     = 50
)

// SyntaxError is a rejected statement. Pos is the 1-based byte position of
// the offending token.
type SyntaxError struct {
	Pos int
	Msg string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("%s (position %d)", e.Msg, e.Pos) }

type rawKwarg struct {
	name  string
	pos   int
	value any
}

type rawPositional struct {
	pos   int
	value any
}

type rawCall struct {
	name       string
	namePos    int
	positional []rawPositional
	kwargs     []rawKwarg
}

type parser struct {
	src string
	i   int
}

func (p *parser) fail(pos int, format string, a ...any) *SyntaxError {
	return &SyntaxError{Pos: pos + 1, Msg: fmt.Sprintf(format, a...)}
}

func (p *parser) eof() bool { return p.i >= len(p.src) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.i]
}

func (p *parser) skipSpace() {
	for !p.eof() {
		switch p.src[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) }

func (p *parser) ident() string {
	start := p.i
	if p.eof() || !isIdentStart(p.src[p.i]) {
		return ""
	}
	for !p.eof() && isIdentPart(p.src[p.i]) {
		p.i++
	}
	return p.src[start:p.i]
}

// unexpected explains the character at the cursor with the most specific
// reason the grammar rejects it.
func (p *parser) unexpected(what string) *SyntaxError {
	if p.eof() {
		return p.fail(p.i, "unexpected end of statement, expected %s", what)
	}
	c := p.src[p.i]
	switch {
	case strings.IndexByte("+-*/%@&|^~<>!=", c) >= 0:
		return p.fail(p.i, "operators are not allowed")
	case c == '#':
		return p.fail(p.i, "comments are not allowed")
	case c == '.':
		return p.fail(p.i, "attribute access is not allowed")
	case c == ';' || c == ':':
		return p.fail(p.i, "only one call is allowed per statement")
	case c == '(' || c == ')':
		return p.fail(p.i, "unexpected %q, expected %s", string(c), what)
	}
	r, _ := utf8.DecodeRuneInString(p.src[p.i:])
	return p.fail(p.i, "unexpected character %q, expected %s", r, what)
}

// parseStatement parses src as one call. src's length was checked by the caller.
func parseStatement(src string) (*rawCall, error) {
	p := &parser{src: src}
	p.skipSpace()
	call := &rawCall{namePos: p.i + 1}
	call.name = p.ident()
	if call.name == "" {
		return nil, p.unexpected("a method name")
	}
	p.skipSpace()
	if p.peek() != '(' {
		return nil, p.unexpected("'(' after the method name")
	}
	p.i++
	for {
		p.skipSpace()
		if p.peek() == ')' {
			p.i++
			break
		}
		if err := p.parseArg(call); err != nil {
			return nil, err
		}
		p.skipSpace()
		switch p.peek() {
		case ',':
			p.i++
		case ')':
		default:
			return nil, p.unexpected("',' or ')'")
		}
	}
	p.skipSpace()
	if !p.eof() {
		return nil, p.unexpected("the end of the statement")
	}
	return call, nil
}

func (p *parser) parseArg(call *rawCall) error {
	start := p.i
	if isIdentStart(p.peek()) {
		name := p.ident()
		p.skipSpace()
		switch {
		case p.peek() == '=' && !strings.HasPrefix(p.src[p.i:], "=="):
			p.i++
			p.skipSpace()
			v, err := p.parseValue(0)
			if err != nil {
				return err
			}
			call.kwargs = append(call.kwargs, rawKwarg{name: name, pos: start + 1, value: v})
			return nil
		case p.peek() == '(':
			return p.fail(p.i, "calls are not allowed in arguments")
		case p.peek() == '.':
			return p.fail(p.i, "attribute access is not allowed")
		}
		v, ok := keywordLiteral(name)
		if !ok {
			return p.fail(start, "%q is not a literal: identifiers are not values", name)
		}
		if len(call.kwargs) > 0 {
			return p.fail(start, "a positional argument cannot follow a keyword argument")
		}
		call.positional = append(call.positional, rawPositional{pos: start + 1, value: v})
		return nil
	}
	v, err := p.parseValue(0)
	if err != nil {
		return err
	}
	if len(call.kwargs) > 0 {
		return p.fail(start, "a positional argument cannot follow a keyword argument")
	}
	call.positional = append(call.positional, rawPositional{pos: start + 1, value: v})
	return nil
}

func keywordLiteral(name string) (any, bool) {
	switch name {
	case "True", "true":
		return true, true
	case "False", "false":
		return false, true
	case "None", "null":
		return nil, true
	}
	return nil, false
}

func (p *parser) parseValue(depth int) (any, error) {
	if p.eof() {
		return nil, p.unexpected("a value")
	}
	c := p.src[p.i]
	switch {
	case c == '"' || c == '\'':
		return p.parseString()
	case c == '[':
		return p.parseList(depth)
	case c == '{':
		return p.parseMap(depth)
	case isDigit(c) || c == '-':
		return p.parseNumber()
	case isIdentStart(c):
		start := p.i
		name := p.ident()
		p.skipSpace()
		switch p.peek() {
		case '(':
			return nil, p.fail(p.i, "calls are not allowed in arguments")
		case '.':
			return nil, p.fail(p.i, "attribute access is not allowed")
		}
		if v, ok := keywordLiteral(name); ok {
			return v, nil
		}
		return nil, p.fail(start, "%q is not a literal: identifiers are not values", name)
	}
	return nil, p.unexpected("a value")
}

func (p *parser) parseString() (any, error) {
	start := p.i
	quote := p.src[p.i]
	p.i++
	var b strings.Builder
	for {
		if p.eof() {
			return nil, p.fail(start, "unterminated string")
		}
		c := p.src[p.i]
		switch {
		case c == quote:
			p.i++
			return b.String(), nil
		case c == '\\':
			p.i++
			if p.eof() {
				return nil, p.fail(start, "unterminated string")
			}
			switch e := p.src[p.i]; e {
			case '\\', '"', '\'':
				b.WriteByte(e)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return nil, p.fail(p.i-1, "escape \\%c is not allowed (only \\\\ \\\" \\' \\n \\t)", e)
			}
			p.i++
		case c < 0x20 && c != '\t', c == 0x7f:
			return nil, p.fail(p.i, "control character in string; use \\n or \\t")
		default:
			r, size := utf8.DecodeRuneInString(p.src[p.i:])
			if r == utf8.RuneError && size == 1 {
				return nil, p.fail(p.i, "string is not valid UTF-8")
			}
			b.WriteString(p.src[p.i : p.i+size])
			p.i += size
		}
	}
}

func (p *parser) parseNumber() (any, error) {
	start := p.i
	if p.peek() == '-' {
		p.i++
		if !isDigit(p.peek()) {
			return nil, p.fail(start, "operators are not allowed")
		}
	}
	for isDigit(p.peek()) {
		p.i++
	}
	float := false
	if p.peek() == '.' {
		float = true
		p.i++
		if !isDigit(p.peek()) {
			return nil, p.fail(start, "malformed number")
		}
		for isDigit(p.peek()) {
			p.i++
		}
	}
	if c := p.peek(); c == 'e' || c == 'E' {
		float = true
		p.i++
		if c := p.peek(); c == '+' || c == '-' {
			p.i++
		}
		if !isDigit(p.peek()) {
			return nil, p.fail(start, "malformed number")
		}
		for isDigit(p.peek()) {
			p.i++
		}
	}
	if isIdentPart(p.peek()) || p.peek() == '.' {
		return nil, p.fail(start, "malformed number")
	}
	text := p.src[start:p.i]
	if !float {
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, p.fail(start, "integer out of range")
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, p.fail(start, "number out of range")
	}
	return f, nil
}

func (p *parser) enter(depth int) (int, error) {
	if depth+1 > MaxDepth {
		return 0, p.fail(p.i, "nesting is deeper than %d levels", MaxDepth)
	}
	return depth + 1, nil
}

func (p *parser) parseList(depth int) (any, error) {
	depth, err := p.enter(depth)
	if err != nil {
		return nil, err
	}
	p.i++
	out := []any{}
	for {
		p.skipSpace()
		if p.peek() == ']' {
			p.i++
			return out, nil
		}
		v, err := p.parseValue(depth)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipSpace()
		switch p.peek() {
		case ',':
			p.i++
		case ']':
		default:
			return nil, p.unexpected("',' or ']'")
		}
	}
}

func (p *parser) parseMap(depth int) (any, error) {
	depth, err := p.enter(depth)
	if err != nil {
		return nil, err
	}
	p.i++
	out := map[string]any{}
	for {
		p.skipSpace()
		if p.peek() == '}' {
			p.i++
			return out, nil
		}
		keyPos := p.i
		if c := p.peek(); c != '"' && c != '\'' {
			return nil, p.fail(p.i, "map keys must be strings")
		}
		k, err := p.parseString()
		if err != nil {
			return nil, err
		}
		key := k.(string)
		if _, dup := out[key]; dup {
			return nil, p.fail(keyPos, "duplicate map key %q", key)
		}
		p.skipSpace()
		if p.peek() != ':' {
			return nil, p.unexpected("':' after the map key")
		}
		p.i++
		p.skipSpace()
		v, err := p.parseValue(depth)
		if err != nil {
			return nil, err
		}
		out[key] = v
		p.skipSpace()
		switch p.peek() {
		case ',':
			p.i++
		case '}':
		default:
			return nil, p.unexpected("',' or '}'")
		}
	}
}
