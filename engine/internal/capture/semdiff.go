package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

// Diff is the semantic difference between two fixtures' model exchanges: what
// a re-recording changed in the requests the agent sent. Responses and the
// trace are not compared. Exchanges holds only the exchanges that differ.
type Diff struct {
	Changed   bool           `json:"changed"`
	Exchanges []ExchangeDiff `json:"exchanges"`
}

// Exchange change kinds.
const (
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
	ChangeChanged = "changed"
)

// ExchangeDiff is one exchange's difference. Index is the exchange's index in
// the new fixture (the old one for a removed exchange); exchanges are paired
// by index.
type ExchangeDiff struct {
	Index       int             `json:"index"`
	Change      string          `json:"change"`
	Label       string          `json:"label"`
	Model       *ValueChange    `json:"model,omitempty"`
	Params      []PathChange    `json:"params,omitempty"`
	System      []LineChange    `json:"system,omitempty"`
	Tools       *ToolsDiff      `json:"tools,omitempty"`
	Messages    []MessageChange `json:"messages,omitempty"`
	ToolResults []MessageChange `json:"tool_results,omitempty"`
}

// ValueChange is a scalar's old and new value.
type ValueChange struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// PathChange is one changed JSON path: Op is added, removed or changed; Old
// and New are the compact JSON of the values.
type PathChange struct {
	Path string `json:"path"`
	Op   string `json:"op"`
	Old  string `json:"old,omitempty"`
	New  string `json:"new,omitempty"`
}

// LineChange is one changed line: Op is "+" or "-".
type LineChange struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// ToolsDiff lists the tool schemas that differ, by tool name.
type ToolsDiff struct {
	Added   []string     `json:"added,omitempty"`
	Removed []string     `json:"removed,omitempty"`
	Changed []ToolChange `json:"changed,omitempty"`
}

// ToolChange is one tool whose schema changed, with the changed JSON paths.
type ToolChange struct {
	Name  string       `json:"name"`
	Paths []PathChange `json:"paths"`
}

// MessageChange is one added, removed or changed message (or tool result).
// Index is the position among the messages (or tool results) of the new
// fixture, of the old one for a removed entry. Ref is the tool call id a tool
// result answers.
type MessageChange struct {
	Index  int          `json:"index"`
	Change string       `json:"change"`
	Role   string       `json:"role"`
	Ref    string       `json:"ref,omitempty"`
	Lines  []LineChange `json:"lines"`
}

// SemanticDiff compares old and new exchange by exchange. Both fixtures are
// read as written (redacted and templated); scrub is applied to every string
// the diff carries, so a value the fixtures' redaction missed never reaches
// the output either.
func SemanticDiff(oldF, newF *Fixture, scrub func(string) string) (*Diff, error) {
	d := &differ{scrub: scrub}
	out := &Diff{Exchanges: []ExchangeDiff{}}
	n := max(len(oldF.Exchanges), len(newF.Exchanges))
	for i := 0; i < n; i++ {
		var ed *ExchangeDiff
		switch {
		case i >= len(oldF.Exchanges):
			nr, err := normalize(&newF.Exchanges[i])
			if err != nil {
				return nil, err
			}
			ed = &ExchangeDiff{Index: i, Change: ChangeAdded, Label: d.s(label(&newF.Exchanges[i], nr))}
		case i >= len(newF.Exchanges):
			or, err := normalize(&oldF.Exchanges[i])
			if err != nil {
				return nil, err
			}
			ed = &ExchangeDiff{Index: i, Change: ChangeRemoved, Label: d.s(label(&oldF.Exchanges[i], or))}
		default:
			var err error
			if ed, err = d.exchange(&oldF.Exchanges[i], &newF.Exchanges[i]); err != nil {
				return nil, err
			}
		}
		if ed != nil {
			out.Exchanges = append(out.Exchanges, *ed)
		}
	}
	out.Changed = len(out.Exchanges) > 0
	return out, nil
}

type differ struct{ scrub func(string) string }

func (d *differ) s(x string) string { return d.scrub(x) }

func label(ex *Exchange, n *normalized) string {
	return strings.Join([]string{ex.Provider, ex.API, n.model}, " ")
}

func (d *differ) exchange(oldEx, newEx *Exchange) (*ExchangeDiff, error) {
	o, err := normalize(oldEx)
	if err != nil {
		return nil, fmt.Errorf("exchange %d (old): %w", oldEx.Index, err)
	}
	n, err := normalize(newEx)
	if err != nil {
		return nil, fmt.Errorf("exchange %d (new): %w", newEx.Index, err)
	}
	ed := &ExchangeDiff{Index: newEx.Index, Change: ChangeChanged, Label: d.s(label(newEx, n))}
	if o.model != n.model {
		ed.Model = &ValueChange{Old: d.s(o.model), New: d.s(n.model)}
	}
	ed.Params = d.paths("$", o.params, n.params)
	ed.System = d.lines(o.system, n.system)
	ed.Tools = d.tools(o.tools, n.tools)
	ed.Messages, ed.ToolResults = d.entries(o.entries, n.entries)
	if ed.Model == nil && ed.Params == nil && ed.System == nil && ed.Tools == nil && ed.Messages == nil && ed.ToolResults == nil {
		return nil, nil
	}
	return ed, nil
}

// normalized is a request reduced to the parts a reviewer reads, whatever the
// provider API.
type normalized struct {
	model   string
	params  map[string]any
	system  []string
	tools   map[string]any
	entries []entry
}

const (
	kindMessage    = "message"
	kindToolResult = "tool_result"
)

// entry is one conversation message or tool result as text lines.
type entry struct {
	role, kind, ref string
	lines           []string
}

func (e entry) key() string {
	return e.kind + "\x00" + e.role + "\x00" + e.ref + "\x00" + strings.Join(e.lines, "\n")
}

func normalize(ex *Exchange) (*normalized, error) {
	var req map[string]any
	dec := json.NewDecoder(bytes.NewReader(ex.Request))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		return nil, fmt.Errorf("request is not a JSON object: %w", err)
	}
	n := &normalized{params: map[string]any{}, tools: map[string]any{}}
	n.model, _ = req["model"].(string)
	used := map[string]bool{"model": true, "tools": true}
	switch ex.API {
	case "chat_completions":
		used["messages"] = true
		n.chatMessages(req["messages"])
	case "responses":
		used["input"], used["instructions"] = true, true
		n.system = append(n.system, textLines(req["instructions"])...)
		n.responsesInput(req["input"])
	case "messages":
		used["system"], used["messages"] = true, true
		n.system = append(n.system, textLines(req["system"])...)
		n.anthropicMessages(req["messages"])
	}
	for k, v := range req {
		if !used[k] {
			n.params[k] = v
		}
	}
	if tools, ok := req["tools"].([]any); ok {
		for i, t := range tools {
			n.tools[toolName(t, i)] = t
		}
	}
	return n, nil
}

func toolName(t any, i int) string {
	m, _ := t.(map[string]any)
	if fn, ok := m["function"].(map[string]any); ok {
		m = fn
	}
	if name, ok := m["name"].(string); ok && name != "" {
		return name
	}
	return fmt.Sprintf("tools[%d]", i)
}

func (n *normalized) add(role, kind, ref string, lines []string) {
	n.entries = append(n.entries, entry{role: role, kind: kind, ref: ref, lines: lines})
}

func (n *normalized) chatMessages(v any) {
	msgs, _ := v.([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		role, _ := m["role"].(string)
		lines := textLines(m["content"])
		switch {
		case systemRole(role):
			n.system = append(n.system, lines...)
		case role == "tool":
			ref, _ := m["tool_call_id"].(string)
			n.add(role, kindToolResult, ref, lines)
		default:
			for _, c := range anySlice(m["tool_calls"]) {
				call, _ := c.(map[string]any)
				fn, _ := call["function"].(map[string]any)
				name, _ := fn["name"].(string)
				lines = append(lines, "tool_call "+name+"("+compact(fn["arguments"])+")")
			}
			n.add(role, kindMessage, "", lines)
		}
	}
}

func (n *normalized) responsesInput(v any) {
	if s, ok := v.(string); ok {
		n.add("user", kindMessage, "", splitLines(s))
		return
	}
	for _, raw := range anySlice(v) {
		m, _ := raw.(map[string]any)
		typ, _ := m["type"].(string)
		role, _ := m["role"].(string)
		switch {
		case typ == "function_call":
			name, _ := m["name"].(string)
			n.add("assistant", kindMessage, "", []string{"tool_call " + name + "(" + compact(m["arguments"]) + ")"})
		case typ == "function_call_output":
			ref, _ := m["call_id"].(string)
			n.add("tool", kindToolResult, ref, textLines(m["output"]))
		case systemRole(role):
			n.system = append(n.system, textLines(m["content"])...)
		default:
			n.add(role, kindMessage, "", textLines(m["content"]))
		}
	}
}

func (n *normalized) anthropicMessages(v any) {
	for _, raw := range anySlice(v) {
		m, _ := raw.(map[string]any)
		role, _ := m["role"].(string)
		blocks, isBlocks := m["content"].([]any)
		if !isBlocks {
			n.add(role, kindMessage, "", textLines(m["content"]))
			continue
		}
		var lines []string
		flush := func() {
			if len(lines) > 0 {
				n.add(role, kindMessage, "", lines)
				lines = nil
			}
		}
		for _, b := range blocks {
			blk, _ := b.(map[string]any)
			switch blk["type"] {
			case "tool_use":
				name, _ := blk["name"].(string)
				lines = append(lines, "tool_use "+name+"("+compact(blk["input"])+")")
			case "tool_result":
				flush()
				ref, _ := blk["tool_use_id"].(string)
				n.add("tool", kindToolResult, ref, textLines(blk["content"]))
			default:
				lines = append(lines, textLines(b)...)
			}
		}
		flush()
	}
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// textLines renders a content value (a string, or a list of content blocks)
// as lines of text.
func textLines(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return splitLines(t)
	case []any:
		var out []string
		for _, b := range t {
			out = append(out, textLines(b)...)
		}
		return out
	case map[string]any:
		if s, ok := t["text"].(string); ok {
			return splitLines(s)
		}
		if typ, ok := t["type"].(string); ok {
			return []string{"[" + typ + "] " + compact(t)}
		}
	}
	return []string{compact(v)}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// compact is the value's compact JSON (a string stays as is); map keys are
// sorted, so equal values render equally.
func compact(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// lines diffs two line lists and returns the removed ("-") and added ("+")
// lines in order; nil when they are equal.
func (d *differ) lines(oldL, newL []string) []LineChange {
	var out []LineChange
	for _, op := range lcsOps(oldL, newL) {
		switch op.kind {
		case opDel:
			out = append(out, LineChange{Op: "-", Text: d.s(oldL[op.i])})
		case opIns:
			out = append(out, LineChange{Op: "+", Text: d.s(newL[op.j])})
		}
	}
	return out
}

const (
	opEq = iota
	opDel
	opIns
)

// lcsOp is one step of an edit script: i indexes the old list (opEq, opDel),
// j the new one (opEq, opIns).
type lcsOp struct{ kind, i, j int }

// lcsOps is the edit script from a to b along a longest common subsequence.
func lcsOps[T comparable](a, b []T) []lcsOp {
	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	var ops []lcsOp
	i, j := 0, 0
	for i < m || j < n {
		switch {
		case i < m && j < n && a[i] == b[j]:
			ops = append(ops, lcsOp{opEq, i, j})
			i++
			j++
		case j >= n || (i < m && dp[i+1][j] >= dp[i][j+1]):
			ops = append(ops, lcsOp{opDel, i, j})
			i++
		default:
			ops = append(ops, lcsOp{opIns, i, j})
			j++
		}
	}
	return ops
}

func (d *differ) paths(prefix string, oldV, newV any) []PathChange {
	var out []PathChange
	d.walk(prefix, oldV, newV, &out)
	return out
}

func (d *differ) walk(path string, oldV, newV any, out *[]PathChange) {
	switch o := oldV.(type) {
	case map[string]any:
		if n, ok := newV.(map[string]any); ok {
			keys := map[string]bool{}
			for k := range o {
				keys[k] = true
			}
			for k := range n {
				keys[k] = true
			}
			sorted := make([]string, 0, len(keys))
			for k := range keys {
				sorted = append(sorted, k)
			}
			sort.Strings(sorted)
			for _, k := range sorted {
				ov, inOld := o[k]
				nv, inNew := n[k]
				d.step(path+"."+k, ov, inOld, nv, inNew, out)
			}
			return
		}
	case []any:
		if n, ok := newV.([]any); ok {
			for i := 0; i < max(len(o), len(n)); i++ {
				var ov, nv any
				if i < len(o) {
					ov = o[i]
				}
				if i < len(n) {
					nv = n[i]
				}
				d.step(fmt.Sprintf("%s[%d]", path, i), ov, i < len(o), nv, i < len(n), out)
			}
			return
		}
	}
	if !reflect.DeepEqual(oldV, newV) {
		*out = append(*out, PathChange{Path: d.s(path), Op: ChangeChanged, Old: d.s(compactJSON(oldV)), New: d.s(compactJSON(newV))})
	}
}

func (d *differ) step(path string, oldV any, inOld bool, newV any, inNew bool, out *[]PathChange) {
	switch {
	case inOld && inNew:
		d.walk(path, oldV, newV, out)
	case inOld:
		*out = append(*out, PathChange{Path: d.s(path), Op: ChangeRemoved, Old: d.s(compactJSON(oldV))})
	default:
		*out = append(*out, PathChange{Path: d.s(path), Op: ChangeAdded, New: d.s(compactJSON(newV))})
	}
}

// compactJSON is the value's JSON, strings quoted, so a changed string and a
// changed number read differently.
func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func (d *differ) tools(oldT, newT map[string]any) *ToolsDiff {
	names := map[string]bool{}
	for k := range oldT {
		names[k] = true
	}
	for k := range newT {
		names[k] = true
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var td ToolsDiff
	for _, name := range sorted {
		ov, inOld := oldT[name]
		nv, inNew := newT[name]
		switch {
		case !inOld:
			td.Added = append(td.Added, d.s(name))
		case !inNew:
			td.Removed = append(td.Removed, d.s(name))
		default:
			if p := d.paths("$", ov, nv); p != nil {
				td.Changed = append(td.Changed, ToolChange{Name: d.s(name), Paths: p})
			}
		}
	}
	if td.Added == nil && td.Removed == nil && td.Changed == nil {
		return nil
	}
	return &td
}

// entries diffs the conversation: entries are matched along a longest common
// subsequence; a removed entry directly followed by an added one of the same
// role and kind is one changed entry with a line diff.
func (d *differ) entries(oldE, newE []entry) (msgs, results []MessageChange) {
	oldK, newK := make([]string, len(oldE)), make([]string, len(newE))
	for i, e := range oldE {
		oldK[i] = e.key()
	}
	for i, e := range newE {
		newK[i] = e.key()
	}
	ops := lcsOps(oldK, newK)
	// position of each entry among the entries of its kind.
	oldPos, newPos := positions(oldE), positions(newE)
	emit := func(c MessageChange, kind string) {
		if kind == kindToolResult {
			results = append(results, c)
		} else {
			msgs = append(msgs, c)
		}
	}
	for k := 0; k < len(ops); {
		if ops[k].kind == opEq {
			k++
			continue
		}
		var dels, inss []int
		for ; k < len(ops) && ops[k].kind != opEq; k++ {
			if ops[k].kind == opDel {
				dels = append(dels, ops[k].i)
			} else {
				inss = append(inss, ops[k].j)
			}
		}
		p := 0
		for ; p < len(dels) && p < len(inss) && pairable(oldE[dels[p]], newE[inss[p]]); p++ {
			o, n := oldE[dels[p]], newE[inss[p]]
			emit(MessageChange{Index: newPos[inss[p]], Change: ChangeChanged, Role: d.s(n.role), Ref: d.s(n.ref), Lines: d.lines(o.lines, n.lines)}, n.kind)
		}
		for _, i := range dels[p:] {
			e := oldE[i]
			emit(MessageChange{Index: oldPos[i], Change: ChangeRemoved, Role: d.s(e.role), Ref: d.s(e.ref), Lines: d.prefixed("-", e.lines)}, e.kind)
		}
		for _, j := range inss[p:] {
			e := newE[j]
			emit(MessageChange{Index: newPos[j], Change: ChangeAdded, Role: d.s(e.role), Ref: d.s(e.ref), Lines: d.prefixed("+", e.lines)}, e.kind)
		}
	}
	return msgs, results
}

func pairable(a, b entry) bool { return a.kind == b.kind && a.role == b.role }

func positions(es []entry) []int {
	out := make([]int, len(es))
	count := map[string]int{}
	for i, e := range es {
		out[i] = count[e.kind]
		count[e.kind]++
	}
	return out
}

func (d *differ) prefixed(op string, lines []string) []LineChange {
	out := make([]LineChange, len(lines))
	for i, l := range lines {
		out[i] = LineChange{Op: op, Text: d.s(l)}
	}
	return out
}

// maxDiffLines caps the lines the text form prints per block.
const maxDiffLines = 20

// WriteText prints the diff for a person: per exchange, +/- lines, each block
// capped at maxDiffLines with a count of the lines left out. Line breaks
// inside a value are escaped; callers escape the remaining control characters.
func (df *Diff) WriteText(w io.Writer, fixtureName string, total int) {
	if !df.Changed {
		fmt.Fprintf(w, "refresh %s: no semantic change\n", oneLine(fixtureName))
		return
	}
	fmt.Fprintf(w, "refresh %s: %d of %d exchanges changed\n", oneLine(fixtureName), len(df.Exchanges), total)
	for _, e := range df.Exchanges {
		fmt.Fprintf(w, "  exchange %d (%s): %s\n", e.Index, oneLine(e.Label), e.Change)
		if e.Model != nil {
			fmt.Fprintf(w, "    model: %s -> %s\n", oneLine(e.Model.Old), oneLine(e.Model.New))
		}
		writePaths(w, "    params:", e.Params)
		writeLines(w, "    system prompt:", "      ", e.System)
		writeTools(w, e.Tools)
		writeMessages(w, "    messages:", e.Messages)
		writeMessages(w, "    tool results:", e.ToolResults)
	}
}

func writePaths(w io.Writer, head string, ps []PathChange) {
	if len(ps) == 0 {
		return
	}
	fmt.Fprintln(w, head)
	for i, p := range ps {
		if i == maxDiffLines {
			fmt.Fprintf(w, "      ... %d more changed paths elided\n", len(ps)-i)
			return
		}
		fmt.Fprintf(w, "      %s\n", pathLine(p))
	}
}

func pathLine(p PathChange) string {
	switch p.Op {
	case ChangeAdded:
		return fmt.Sprintf("+ %s: %s", oneLine(p.Path), oneLine(ellipsize(p.New)))
	case ChangeRemoved:
		return fmt.Sprintf("- %s: %s", oneLine(p.Path), oneLine(ellipsize(p.Old)))
	}
	return fmt.Sprintf("~ %s: %s -> %s", oneLine(p.Path), oneLine(ellipsize(p.Old)), oneLine(ellipsize(p.New)))
}

func ellipsize(s string) string {
	r := []rune(s)
	if len(r) <= 80 {
		return s
	}
	return string(r[:80]) + "..."
}

func writeLines(w io.Writer, head, indent string, ls []LineChange) {
	if len(ls) == 0 {
		return
	}
	if head != "" {
		fmt.Fprintln(w, head)
	}
	for i, l := range ls {
		if i == maxDiffLines {
			fmt.Fprintf(w, "%s... %d more changed lines elided\n", indent, len(ls)-i)
			return
		}
		fmt.Fprintf(w, "%s%s %s\n", indent, l.Op, oneLine(l.Text))
	}
}

func writeTools(w io.Writer, t *ToolsDiff) {
	if t == nil {
		return
	}
	fmt.Fprintln(w, "    tools:")
	for _, n := range t.Added {
		fmt.Fprintf(w, "      + %s\n", oneLine(n))
	}
	for _, n := range t.Removed {
		fmt.Fprintf(w, "      - %s\n", oneLine(n))
	}
	for _, c := range t.Changed {
		fmt.Fprintf(w, "      ~ %s\n", oneLine(c.Name))
		for i, p := range c.Paths {
			if i == maxDiffLines {
				fmt.Fprintf(w, "          ... %d more changed paths elided\n", len(c.Paths)-i)
				break
			}
			fmt.Fprintf(w, "          %s\n", pathLine(p))
		}
	}
}

func writeMessages(w io.Writer, head string, ms []MessageChange) {
	if len(ms) == 0 {
		return
	}
	fmt.Fprintln(w, head)
	sym := map[string]string{ChangeAdded: "+", ChangeRemoved: "-", ChangeChanged: "~"}
	for _, m := range ms {
		ref := ""
		if m.Ref != "" {
			ref = " " + oneLine(m.Ref)
		}
		fmt.Fprintf(w, "      %s %s%s [%d]\n", sym[m.Change], oneLine(m.Role), ref, m.Index)
		writeLines(w, "", "        ", m.Lines)
	}
}

func systemRole(role string) bool { return role == "system" || role == "developer" }

// oneLine keeps a value on its line: a line break inside a name or path would
// forge a diff line.
func oneLine(s string) string {
	return strings.NewReplacer("\n", `\n`, "\r", `\r`).Replace(s)
}
