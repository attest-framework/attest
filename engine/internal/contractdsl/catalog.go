package contractdsl

import (
	"fmt"
	"math"
	"sort"

	"github.com/attest-framework/attest/engine/pkg/types"
)

type kind int

const (
	kString kind = iota
	kInt
	kNumber
	kBool
	kStrings   // list of strings
	kStringsOr // a string or a list of strings; the spec holds a list
	kPairs     // list of [before, after] string pairs
	kObject    // string-keyed map
	kValue     // any literal
	kRelated   // a related(resource, field, on=key) reference
	kBound     // a number or a related(...) reference
)

// Related is the closed grammar's one non-literal form, related(resource,
// field, on=key): the record in resource whose key field equals the key of the
// record being checked, and the field to read from it.
type Related struct {
	Resource string `json:"resource"`
	Field    string `json:"field"`
	On       string `json:"on"`
}

func (r Related) specMap() map[string]any {
	return map[string]any{"resource": r.Resource, "field": r.Field, "on": r.On}
}

// relatedFromSpec reads the {resource, field, on} map of a spec.
func relatedFromSpec(v any) (Related, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 3 {
		return Related{}, false
	}
	var r Related
	for k, dst := range map[string]*string{"resource": &r.Resource, "field": &r.Field, "on": &r.On} {
		s, ok := m[k].(string)
		if !ok || s == "" {
			return Related{}, false
		}
		*dst = s
	}
	return r, true
}

func (k kind) String() string {
	switch k {
	case kString:
		return "a string"
	case kInt:
		return "an integer"
	case kNumber:
		return "a number"
	case kBool:
		return "a boolean"
	case kStrings:
		return "a list of strings"
	case kStringsOr:
		return "a string or a list of strings"
	case kPairs:
		return "a list of [before, after] string pairs"
	case kObject:
		return "a map with string keys"
	case kRelated:
		return "a related(resource, field, on=key) reference"
	case kBound:
		return "a number or a related(resource, field, on=key) reference"
	}
	return "a literal"
}

// param is one argument of a catalog method.
type param struct {
	name       string // Python name, also the identifier in the catalog
	ts         string // TypeScript option name; empty means same as name
	specKey    string // key in the assertion spec; empty for where
	kind       kind
	positional bool
	required   bool
	// def is applied to the spec when the argument is absent. A call built
	// from a spec leaves out arguments that hold their default.
	def     any
	enum    []string
	min     int64
	hasMin  bool
	isWhere bool
}

func (p param) tsName() string {
	if p.ts != "" {
		return p.ts
	}
	return p.name
}

func soft() param {
	return param{name: "soft", specKey: "soft", kind: kBool, def: false}
}

// Method is one callable of the closed catalog.
type Method struct {
	Name   string // Python snake_case name
	TS     string // TypeScript camelCase name
	Type   string // assertion type of the spec
	Effect bool   // an effect check: extra keywords are record filters and the spec needs a world
	fixed  map[string]any
	params []param
	// check validates the bound arguments of the whole call.
	check func(args map[string]any) error
}

var traceModes = []string{"strict", "unordered", "subset", "superset"}
var argOps = []string{"eq", "ne", "lt", "lte", "gt", "gte", "in", "not_in"}

func atLeastOne(names ...string) func(map[string]any) error {
	return func(a map[string]any) error {
		for _, n := range names {
			if _, ok := a[n]; ok {
				return nil
			}
		}
		return fmt.Errorf("needs at least one of %v", names)
	}
}

var catalog = []*Method{
	{
		Name: "tool_cardinality", TS: "toolCardinality", Type: types.TypeTrace,
		fixed: map[string]any{"check": "tool_cardinality"},
		params: []param{
			{name: "tool", specKey: "tool", kind: kString, positional: true, required: true},
			{name: "per", specKey: "per", kind: kString},
			{name: "at_most", ts: "atMost", specKey: "max", kind: kInt, hasMin: true},
			{name: "at_least", ts: "atLeast", specKey: "min", kind: kInt, hasMin: true},
			soft(),
		},
		check: atLeastOne("at_most", "at_least"),
	},
	{
		Name: "required_tools", TS: "requiredTools", Type: types.TypeTrace,
		fixed: map[string]any{"check": "required_tools"},
		params: []param{
			{name: "tools", specKey: "tools", kind: kStrings, positional: true, required: true},
			soft(),
		},
	},
	{
		Name: "forbidden_tools", TS: "forbiddenTools", Type: types.TypeTrace,
		fixed: map[string]any{"check": "forbidden_tools"},
		params: []param{
			{name: "tools", specKey: "tools", kind: kStrings, positional: true, required: true},
			soft(),
		},
	},
	{
		Name: "trajectory", TS: "trajectory", Type: types.TypeTrace,
		fixed: map[string]any{"check": "trajectory"},
		params: []param{
			{name: "tools", specKey: "tools", kind: kStrings, positional: true, required: true},
			{name: "mode", specKey: "mode", kind: kString, def: "strict", enum: traceModes},
			soft(),
		},
	},
	{
		Name: "trajectory_partial_order", TS: "trajectoryPartialOrder", Type: types.TypeTrace,
		fixed: map[string]any{"check": "trajectory", "mode": "partial_order"},
		params: []param{
			{name: "order", specKey: "order", kind: kPairs, positional: true, required: true},
			{name: "tools", specKey: "tools", kind: kStrings},
			soft(),
		},
	},
	{
		Name: "tool_called_before", TS: "toolCalledBefore", Type: types.TypeTrace,
		fixed: map[string]any{"check": "tool_order"},
		params: []param{
			{name: "before", specKey: "before", kind: kString, positional: true, required: true},
			{name: "after", specKey: "after", kind: kString, positional: true, required: true},
			soft(),
		},
	},
	{
		Name: "no_tool_cycles", TS: "noToolCycles", Type: types.TypeTrace,
		fixed: map[string]any{"check": "cycle"},
		params: []param{
			{name: "period", specKey: "period", kind: kInt, positional: true, required: true, hasMin: true, min: 1},
			{name: "min_repeats", ts: "minRepeats", specKey: "min_repeats", kind: kInt, def: int64(2), hasMin: true, min: 2},
			soft(),
		},
	},
	{
		Name: "max_tool_failure_streak", TS: "maxToolFailureStreak", Type: types.TypeTrace,
		fixed: map[string]any{"check": "failure_streak"},
		params: []param{
			{name: "max_streak", ts: "maxStreak", specKey: "max", kind: kInt, positional: true, required: true, hasMin: true},
			soft(),
		},
	},
	{
		Name: "tool_arg_satisfies", TS: "toolArgSatisfies", Type: types.TypeTrace,
		fixed: map[string]any{"check": "arg_predicate"},
		params: []param{
			{name: "tool", specKey: "tool", kind: kString, positional: true, required: true},
			{name: "arg", specKey: "arg", kind: kString, positional: true, required: true},
			{name: "op", specKey: "op", kind: kString, positional: true, required: true, enum: argOps},
			{name: "value", specKey: "value", kind: kValue, positional: true, required: true},
			soft(),
		},
	},
	{
		Name: "output_matches_schema", TS: "outputMatchesSchema", Type: types.TypeSchema,
		fixed: map[string]any{"target": "output.structured"},
		params: []param{
			{name: "schema", specKey: "schema", kind: kObject, positional: true, required: true},
		},
	},
	{
		Name: "cost_under", TS: "costUnder", Type: types.TypeConstraint,
		fixed: map[string]any{"field": "metadata.cost_usd", "operator": "lte"},
		params: []param{
			{name: "max_cost", ts: "maxCost", specKey: "value", kind: kNumber, positional: true, required: true},
			soft(),
		},
	},
	{
		Name: "count", TS: "count", Type: types.TypeEffect, Effect: true,
		fixed: map[string]any{"check": "count"},
		params: []param{
			{name: "resource", specKey: "resource", kind: kString, positional: true, required: true},
			{name: "equals", specKey: "equals", kind: kInt, hasMin: true},
			{name: "at_most", ts: "atMost", specKey: "at_most", kind: kInt, hasMin: true},
			{name: "at_least", ts: "atLeast", specKey: "at_least", kind: kInt, hasMin: true},
			{name: "where", kind: kObject, isWhere: true},
		},
		check: func(a map[string]any) error {
			_, eq := a["equals"]
			_, hi := a["at_most"]
			_, lo := a["at_least"]
			switch {
			case !eq && !hi && !lo:
				return fmt.Errorf("needs equals, at_most or at_least")
			case eq && (hi || lo):
				return fmt.Errorf("equals excludes at_most and at_least")
			}
			return nil
		},
	},
	{
		Name: "unique", TS: "unique", Type: types.TypeEffect, Effect: true,
		fixed: map[string]any{"check": "unique"},
		params: []param{
			{name: "resource", specKey: "resource", kind: kString, positional: true, required: true},
			{name: "by", specKey: "fields", kind: kStringsOr, required: true},
			{name: "where", kind: kObject, isWhere: true},
		},
	},
	{
		Name: "idempotent", TS: "idempotent", Type: types.TypeEffect, Effect: true,
		fixed: map[string]any{"check": "idempotent"},
		params: []param{
			{name: "resource", specKey: "resource", kind: kString, positional: true, required: true},
			{name: "key", specKey: "field", kind: kString, def: "idempotency_key"},
			{name: "where", kind: kObject, isWhere: true},
		},
	},
	{
		Name: "sum", TS: "sum", Type: types.TypeEffect, Effect: true,
		fixed: map[string]any{"check": "sum"},
		params: []param{
			{name: "resource", specKey: "resource", kind: kString, positional: true, required: true},
			{name: "field", specKey: "field", kind: kString, positional: true, required: true},
			{name: "at_most", ts: "atMost", specKey: "at_most", kind: kBound, required: true},
			{name: "where", kind: kObject, isWhere: true},
		},
	},
	{
		Name: "matches", TS: "matches", Type: types.TypeEffect, Effect: true,
		fixed: map[string]any{"check": "matches"},
		params: []param{
			{name: "resource", specKey: "resource", kind: kString, positional: true, required: true},
			{name: "field", specKey: "field", kind: kString, positional: true, required: true},
			{name: "related", specKey: "related", kind: kRelated, positional: true, required: true},
			{name: "where", kind: kObject, isWhere: true},
		},
	},
}

// Methods returns the catalog in a fixed order.
func Methods() []*Method {
	out := make([]*Method, len(catalog))
	copy(out, catalog)
	return out
}

// Lookup finds a method by its Python or TypeScript name.
func Lookup(name string) (*Method, bool) {
	for _, m := range catalog {
		if m.Name == name || m.TS == name {
			return m, true
		}
	}
	return nil, false
}

func (m *Method) param(name string) (param, bool) {
	for _, p := range m.params {
		if p.name == name || p.tsName() == name {
			return p, true
		}
	}
	return param{}, false
}

// Signature lists the method's parameters for tool descriptions.
func (m *Method) Signature() string {
	s := m.Name + "("
	for i, p := range m.params {
		if i > 0 {
			s += ", "
		}
		switch {
		case p.positional:
			s += p.name
		case p.required:
			s += p.name + "=…"
		default:
			s += "[" + p.name + "=…]"
		}
	}
	if m.Effect {
		s += ", [<field>=…]"
	}
	return s + ")"
}

// coerce checks v against the parameter and returns its normalized value.
func (p param) coerce(v any) (any, error) {
	bad := func() (any, error) { return nil, fmt.Errorf("argument %q must be %s", p.name, p.kind) }
	if _, isRelated := v.(Related); isRelated && p.kind != kRelated && p.kind != kBound {
		return nil, fmt.Errorf("argument %q cannot be a related(...) reference; it takes %s", p.name, p.kind)
	}
	switch p.kind {
	case kString:
		s, ok := v.(string)
		if !ok {
			return bad()
		}
		if len(p.enum) > 0 && !contains(p.enum, s) {
			return nil, fmt.Errorf("argument %q must be one of %v", p.name, p.enum)
		}
		return s, nil
	case kInt:
		n, ok := asInt(v)
		if !ok {
			return bad()
		}
		if p.hasMin && n < p.min {
			return nil, fmt.Errorf("argument %q must be at least %d", p.name, p.min)
		}
		return n, nil
	case kNumber:
		switch x := v.(type) {
		case int64:
			return x, nil
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return bad()
			}
			return x, nil
		}
		return bad()
	case kBool:
		b, ok := v.(bool)
		if !ok {
			return bad()
		}
		return b, nil
	case kRelated:
		return p.coerceRelated(v, bad)
	case kBound:
		if m, ok := v.(map[string]any); ok && len(m) == 1 {
			if inner, ok := m["related"]; ok {
				return p.coerceRelated(inner, bad)
			}
		}
		if _, ok := v.(Related); ok {
			return p.coerceRelated(v, bad)
		}
		return param{name: p.name, kind: kNumber}.coerce(v)
	case kStrings:
		l, ok := stringList(v)
		if !ok {
			return bad()
		}
		return l, nil
	case kStringsOr:
		if s, ok := v.(string); ok {
			return []any{s}, nil
		}
		l, ok := stringList(v)
		if !ok || len(l) == 0 {
			return bad()
		}
		return l, nil
	case kPairs:
		l, ok := v.([]any)
		if !ok {
			return bad()
		}
		for _, e := range l {
			pair, ok := stringList(e)
			if !ok || len(pair) != 2 {
				return bad()
			}
		}
		return l, nil
	case kObject:
		m, ok := v.(map[string]any)
		if !ok {
			return bad()
		}
		return m, nil
	}
	return v, nil
}

func (p param) coerceRelated(v any, bad func() (any, error)) (any, error) {
	switch x := v.(type) {
	case Related:
		if x.Resource == "" || x.Field == "" || x.On == "" {
			return bad()
		}
		return x, nil
	case map[string]any:
		if r, ok := relatedFromSpec(x); ok {
			return r, nil
		}
	}
	return bad()
}

// specValue is the form a bound argument takes in an assertion spec.
func (p param) specValue(v any) any {
	r, ok := v.(Related)
	if !ok {
		return v
	}
	if p.kind == kBound {
		return map[string]any{"related": r.specMap()}
	}
	return r.specMap()
}

func asInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x), true
		}
	}
	return 0, false
}

func stringList(v any) ([]any, bool) {
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	for _, e := range l {
		if _, ok := e.(string); !ok {
			return nil, false
		}
	}
	return l, true
}

func contains(l []string, s string) bool {
	for _, e := range l {
		if e == s {
			return true
		}
	}
	return false
}

// Call is one parsed statement: a catalog method with validated arguments.
type Call struct {
	Method *Method
	// Args holds the arguments by Python parameter name.
	Args map[string]any
	// Filters holds the exact-match record filters of an effect check.
	Filters map[string]any
}

// Parse reads one statement and binds it to the catalog. It never evaluates
// anything: a statement that is not exactly a catalog call is an error.
func Parse(statement string) (*Call, error) {
	if len(statement) > MaxStatementBytes {
		return nil, &SyntaxError{Pos: MaxStatementBytes + 1, Msg: fmt.Sprintf("statement is longer than %d bytes", MaxStatementBytes)}
	}
	raw, err := parseStatement(statement)
	if err != nil {
		return nil, err
	}
	m, ok := Lookup(raw.name)
	if !ok {
		return nil, &SyntaxError{Pos: raw.namePos, Msg: fmt.Sprintf("unknown method %q", raw.name)}
	}
	return bind(m, raw)
}

func bind(m *Method, raw *rawCall) (*Call, error) {
	c := &Call{Method: m, Args: map[string]any{}, Filters: map[string]any{}}
	var positional []param
	for _, p := range m.params {
		if p.positional {
			positional = append(positional, p)
		}
	}
	fail := func(pos int, format string, a ...any) error {
		return &SyntaxError{Pos: pos, Msg: fmt.Sprintf(format, a...)}
	}
	set := func(pos int, p param, v any) error {
		if _, dup := c.Args[p.name]; dup {
			return fail(pos, "argument %q is given twice", p.name)
		}
		nv, err := p.coerce(v)
		if err != nil {
			return fail(pos, "%s: %v", m.Name, err)
		}
		c.Args[p.name] = nv
		return nil
	}
	for i, a := range raw.positional {
		if i >= len(positional) {
			return nil, fail(a.pos, "%s takes %d positional arguments", m.Name, len(positional))
		}
		if err := set(a.pos, positional[i], a.value); err != nil {
			return nil, err
		}
	}
	var where map[string]any
	for _, kw := range raw.kwargs {
		p, known := m.param(kw.name)
		switch {
		case known && p.isWhere:
			obj, err := p.coerce(kw.value)
			if err != nil {
				return nil, fail(kw.pos, "%s: %v", m.Name, err)
			}
			if where != nil {
				return nil, fail(kw.pos, "argument %q is given twice", p.name)
			}
			where = obj.(map[string]any)
		case known:
			if err := set(kw.pos, p, kw.value); err != nil {
				return nil, err
			}
		case m.Effect:
			if _, dup := c.Filters[kw.name]; dup {
				return nil, fail(kw.pos, "filter %q is given twice", kw.name)
			}
			if _, isRelated := kw.value.(Related); isRelated {
				return nil, fail(kw.pos, "filter %q cannot be a related(...) reference; filters are literals", kw.name)
			}
			c.Filters[kw.name] = kw.value
		default:
			return nil, fail(kw.pos, "%s has no argument %q", m.Name, kw.name)
		}
	}
	for k, v := range where {
		if _, dup := c.Filters[k]; dup {
			return nil, fail(raw.namePos, "filter %q is given both in where and as a keyword", k)
		}
		c.Filters[k] = v
	}
	return c, c.validate(raw.namePos)
}

func (c *Call) validate(pos int) error {
	for _, p := range c.Method.params {
		if p.required {
			if _, ok := c.Args[p.name]; !ok {
				return &SyntaxError{Pos: pos, Msg: fmt.Sprintf("%s: missing argument %q", c.Method.Name, p.name)}
			}
		}
	}
	if c.Method.check != nil {
		if err := c.Method.check(c.Args); err != nil {
			return &SyntaxError{Pos: pos, Msg: fmt.Sprintf("%s: %v", c.Method.Name, err)}
		}
	}
	return nil
}

// Type is the assertion type of the call's spec.
func (c *Call) Type() string { return c.Method.Type }

// Spec builds the assertion spec the call emits. An effect spec carries no
// world; the caller adds the snapshot to evaluate it.
func (c *Call) Spec() map[string]any {
	spec := map[string]any{}
	for k, v := range c.Method.fixed {
		spec[k] = v
	}
	for _, p := range c.Method.params {
		if p.specKey == "" {
			continue
		}
		v, ok := c.Args[p.name]
		if !ok {
			if p.def == nil {
				continue
			}
			v = p.def
		}
		spec[p.specKey] = p.specValue(v)
	}
	if len(c.Filters) > 0 {
		spec["where"] = c.Filters
	}
	return spec
}

// FromSpec is the inverse of Spec: it finds the catalog method that emits spec
// and recovers its arguments. The spec's world, if any, is ignored.
func FromSpec(typ string, spec map[string]any) (*Call, error) {
	methods := append([]*Method(nil), catalog...)
	sort.SliceStable(methods, func(i, j int) bool { return len(methods[i].fixed) > len(methods[j].fixed) })
	for _, m := range methods {
		if m.Type != typ || !m.matches(spec) {
			continue
		}
		return m.fromSpec(spec)
	}
	return nil, fmt.Errorf("no statement form for a %s spec", typ)
}

func (m *Method) matches(spec map[string]any) bool {
	for k, want := range m.fixed {
		got, ok := spec[k]
		if !ok || fmt.Sprint(got) != fmt.Sprint(want) {
			return false
		}
	}
	return true
}

func (m *Method) fromSpec(spec map[string]any) (*Call, error) {
	c := &Call{Method: m, Args: map[string]any{}, Filters: map[string]any{}}
	known := map[string]bool{"world": true, "where": true}
	for k := range m.fixed {
		known[k] = true
	}
	for _, p := range m.params {
		if p.specKey == "" {
			continue
		}
		known[p.specKey] = true
		v, ok := spec[p.specKey]
		if !ok || v == nil {
			continue
		}
		nv, err := p.coerce(v)
		if err != nil {
			return nil, fmt.Errorf("%s spec: %w", m.Name, err)
		}
		if p.def != nil && fmt.Sprint(nv) == fmt.Sprint(p.def) {
			continue
		}
		c.Args[p.name] = nv
	}
	for k := range spec {
		if !known[k] {
			return nil, fmt.Errorf("%s spec has key %q, which the statement form cannot carry", m.Name, k)
		}
	}
	if w, ok := spec["where"]; ok && w != nil {
		obj, ok := w.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s spec: where must be a map", m.Name)
		}
		for k, v := range obj {
			c.Filters[k] = v
		}
	}
	return c, c.validate(0)
}
