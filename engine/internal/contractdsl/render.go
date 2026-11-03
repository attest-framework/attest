package contractdsl

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Form is a call in structured form: a method, positional arguments and
// keyword arguments (Python) or a trailing options object (TypeScript).
type Form struct {
	Method string         `json:"method"`
	Args   []any          `json:"args"`
	Kwargs map[string]any `json:"kwargs,omitempty"`
}

var pythonKeywords = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true, "assert": true, "async": true,
	"await": true, "break": true, "class": true, "continue": true, "def": true, "del": true, "elif": true,
	"else": true, "except": true, "finally": true, "for": true, "from": true, "global": true, "if": true,
	"import": true, "in": true, "is": true, "lambda": true, "nonlocal": true, "not": true, "or": true,
	"pass": true, "raise": true, "return": true, "try": true, "while": true, "with": true, "yield": true,
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// rendered lists the arguments the call carries, in parameter order.
func (c *Call) rendered() (positional []any, keyword []namedValue) {
	for _, p := range c.Method.params {
		if p.isWhere {
			continue
		}
		v, ok := c.Args[p.name]
		if !ok {
			continue
		}
		if p.kind == kStringsOr {
			if l := v.([]any); len(l) == 1 {
				v = l[0]
			}
		}
		if p.positional {
			positional = append(positional, v)
		} else {
			keyword = append(keyword, namedValue{p, v})
		}
	}
	return positional, keyword
}

type namedValue struct {
	p param
	v any
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Python is the call as the Python SDK writes it. Record filters are keyword
// arguments, or one where= map when a filter name is not a plain identifier or
// collides with a parameter or keyword.
func (c *Call) Python() Form {
	pos, kw := c.rendered()
	f := Form{Method: c.Method.Name, Args: pos, Kwargs: map[string]any{}}
	if pos == nil {
		f.Args = []any{}
	}
	for _, n := range kw {
		f.Kwargs[n.p.name] = n.v
	}
	inline := true
	for k := range c.Filters {
		if _, clash := c.Method.param(k); clash || !identRe.MatchString(k) || pythonKeywords[k] {
			inline = false
		}
	}
	switch {
	case len(c.Filters) == 0:
	case inline:
		for k, v := range c.Filters {
			f.Kwargs[k] = v
		}
	default:
		f.Kwargs["where"] = c.Filters
	}
	return f
}

// TypeScript is the call as the TypeScript SDK writes it: positional
// arguments, then one options object when any option is set.
func (c *Call) TypeScript() Form {
	pos, kw := c.rendered()
	f := Form{Method: c.Method.TS, Args: append([]any{}, pos...)}
	opts := map[string]any{}
	for _, n := range kw {
		opts[n.p.tsName()] = n.v
	}
	if len(c.Filters) > 0 {
		opts["where"] = c.Filters
	}
	if len(opts) > 0 {
		f.Args = append(f.Args, opts)
	}
	return f
}

// Statement renders the call in the closed grammar, so Parse reads it back.
// A string with a character the grammar has no escape for (a control or
// invisible character) has no statement form.
func (c *Call) Statement() (string, error) { return c.pythonCall(grammarString) }

func (c *Call) pythonCall(st style) (string, error) {
	f := c.Python()
	var b strings.Builder
	b.WriteString(f.Method + "(")
	n := 0
	sep := func() {
		if n > 0 {
			b.WriteString(", ")
		}
		n++
	}
	for _, a := range f.Args {
		sep()
		if err := writeLiteral(&b, a, st); err != nil {
			return "", err
		}
	}
	for _, name := range c.keywordOrder(f) {
		sep()
		b.WriteString(name + "=")
		if err := writeLiteral(&b, f.Kwargs[name], st); err != nil {
			return "", err
		}
	}
	b.WriteString(")")
	return b.String(), nil
}

// ChainPrefix is what must precede the call in a contract chain: effect checks
// exist only on the object returned by .world(snapshot), in both SDKs. FIXTURE
// is the variable `attest capture` generated tests load the fixture into; use
// another snapshot expression where the test holds the world elsewhere.
func (c *Call) ChainPrefix() string {
	if c.Method.Effect {
		return ".world(FIXTURE.world)"
	}
	return ""
}

// keywordOrder lists the Python keyword arguments in parameter order, then
// filters sorted by name.
func (c *Call) keywordOrder(f Form) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range c.Method.params {
		if _, ok := f.Kwargs[p.name]; ok && !p.positional {
			out = append(out, p.name)
			seen[p.name] = true
		}
	}
	var rest []string
	for k := range f.Kwargs {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// PythonLine is the chain line to paste into a pytest contract: the chain
// prefix (effect checks only) and ".method(...)". Characters that are invisible
// or reorder text are written as \u escapes.
func (c *Call) PythonLine() (string, error) {
	s, err := c.pythonCall(pythonLine)
	if err != nil {
		return "", err
	}
	return c.ChainPrefix() + "." + s, nil
}

// TypeScriptLine is the chain line to paste into a Vitest contract.
func (c *Call) TypeScriptLine() (string, error) {
	pos, kw := c.rendered()
	var b strings.Builder
	b.WriteString(c.ChainPrefix() + "." + c.Method.TS + "(")
	n := 0
	for _, a := range pos {
		if n > 0 {
			b.WriteString(", ")
		}
		n++
		if err := writeLiteral(&b, a, tsObject); err != nil {
			return "", err
		}
	}
	if len(kw) > 0 || len(c.Filters) > 0 {
		if n > 0 {
			b.WriteString(", ")
		}
		b.WriteString("{ ")
		for i, o := range kw {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(o.p.tsName() + ": ")
			if err := writeLiteral(&b, o.v, tsObject); err != nil {
				return "", err
			}
		}
		if len(c.Filters) > 0 {
			if len(kw) > 0 {
				b.WriteString(", ")
			}
			b.WriteString("where: ")
			if err := writeMap(&b, c.Filters, tsObject); err != nil {
				return "", err
			}
		}
		b.WriteString(" }")
	}
	b.WriteString(")")
	return b.String(), nil
}

type style int

const (
	grammarString style = iota // Python literals, strings limited to the grammar's escapes
	pythonLine                 // Python literals for a pasted line: any string, with \u escapes
	tsObject                   // TypeScript literals
)

func writeLiteral(b *strings.Builder, v any, st style) error {
	switch x := v.(type) {
	case nil:
		if st == tsObject {
			b.WriteString("null")
		} else {
			b.WriteString("None")
		}
	case bool:
		switch {
		case st == tsObject && x:
			b.WriteString("true")
		case st == tsObject:
			b.WriteString("false")
		case x:
			b.WriteString("True")
		default:
			b.WriteString("False")
		}
	case string:
		s, err := quote(x, st)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case float64:
		b.WriteString(formatFloat(x, st))
	case json.Number:
		b.WriteString(x.String())
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := writeLiteral(b, e, st); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		return writeMap(b, x, st)
	default:
		return fmt.Errorf("value of type %T has no literal form", v)
	}
	return nil
}

func writeMap(b *strings.Builder, m map[string]any, st style) error {
	if len(m) == 0 {
		b.WriteString("{}")
		return nil
	}
	b.WriteString("{ ")
	for i, k := range sortedKeys(m) {
		if i > 0 {
			b.WriteString(", ")
		}
		switch {
		case st == tsObject && k == "__proto__":
			b.WriteString(`["__proto__"]`)
		case st == tsObject && identRe.MatchString(k):
			b.WriteString(k)
		default:
			q, err := quote(k, st)
			if err != nil {
				return err
			}
			b.WriteString(q)
		}
		b.WriteString(": ")
		if err := writeLiteral(b, m[k], st); err != nil {
			return err
		}
	}
	b.WriteString(" }")
	return nil
}

func formatFloat(f float64, st style) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if st != tsObject && !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// needsEscape reports a character that must not appear raw in a pasted line:
// controls, Unicode format characters (bidirectional controls, zero-width and
// soft-hyphen characters, the tag block), and line and paragraph separators.
func needsEscape(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0xFFFD ||
		unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

// quote renders s as a double-quoted literal. The grammar knows five escapes,
// so a string needing any other has no statement form; the pasted lines write
// those characters as \u escapes instead.
func quote(s string, st style) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\' || r == '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case !needsEscape(r):
			b.WriteRune(r)
		case st == grammarString:
			return "", fmt.Errorf("string contains a character that has no statement form")
		case r > 0xFFFF && st == tsObject:
			fmt.Fprintf(&b, `\u{%x}`, r)
		case r > 0xFFFF:
			fmt.Fprintf(&b, `\U%08x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}
