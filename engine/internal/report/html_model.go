package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/attest-framework/attest/engine/internal/mutate"
	"github.com/attest-framework/attest/engine/pkg/types"
)

// Bounds on what the viewer reads and renders. Report content is untrusted: a
// trace can hold arbitrarily large strings, so every rendered string is cut to
// viewMaxString bytes and the page carries at most viewMaxTotalBytes of report
// text. What was cut is named in View.Notices.
const (
	viewMaxInputBytes    = 256 << 20
	viewMaxString        = 4 << 10
	viewMaxTotalBytes    = 8 << 20
	viewMaxTests         = 1000
	viewMaxFindingRows   = 200
	viewMaxTracesPerTest = 5
	viewMaxStepsPerTrace = 500
	viewMaxSteps         = 3000
	viewMaxDepth         = 6
	viewMaxSequence      = 200
	viewMaxListed        = 200
	viewMaxNotices       = 50
	viewMaxNoticeBytes   = 300
	viewMaxIndentDepth   = 32
)

// Test classes in View output; they pick a CSS class and nothing else.
const (
	classPass = "pass"
	classFail = "fail"
	classNE   = "ne"
	classSkip = "skip"
)

// ViewOptions are the inputs of `attest view`.
type ViewOptions struct {
	Reports   []string
	Baselines []string
	Mutation  string
}

// View is everything the page shows, with every string already bounded.
type View struct {
	Summary       SummaryView
	Notices       []string
	TracesMissing bool
	Tests         []TestView
	Baseline      *BaselineView
	Mutation      *MutationView

	// Failures and TraceCount feed `view --json`.
	Failures   int
	TraceCount int
}

// SummaryView is the run overview.
type SummaryView struct {
	Tests          int
	Passed         int
	Failed         int
	NotEvaluated   int
	Skipped        int
	NotEvaluatedBy []string
	Assertions     string
	PassRate       string
	Replay         string
	Cost           string
	Duration       string
	Simulated      bool
}

// TestView is one test with its findings and traces.
type TestView struct {
	Anchor     string
	Name       string
	Location   string
	Class      string
	Label      string
	Duration   string
	Provenance string
	Open       bool
	Findings   []FindingView
	PassedIDs  []string
	RowsCut    int
	Traces     []TraceView
}

// FindingView is one assertion that did not pass.
type FindingView struct {
	Anchor      string
	AssertionID string
	Type        string
	Status      string
	Class       string
	Reason      string
	Explanation string
	Expected    string
	Actual      string
	Path        string
	Cost        string
	Provenance  string
	StepAnchor  string
}

// TraceView is one trace as a tree of steps.
type TraceView struct {
	Anchor   string
	ID       string
	AgentID  string
	Input    string
	Output   string
	Sequence string
	Steps    []StepView
}

// StepView is one step; Children are the steps of its sub-trace.
type StepView struct {
	Anchor    string
	Index     int
	Path      string
	Type      string
	Name      string
	Args      string
	Result    string
	Error     string
	Highlight bool
	Marks     []MarkView
	Children  []StepView
	Omitted   int
}

// MarkView links a highlighted step to the failing assertion.
type MarkView struct {
	Anchor string
	Label  string
}

// BaselineView compares the head reports with the baseline reports.
type BaselineView struct {
	Rows     []DiffRow
	Tests    []TestDiff
	NoTraces bool
}

// DiffRow is one metric of the comparison.
type DiffRow struct {
	Metric string
	Base   string
	Head   string
	Delta  string
	Class  string
}

// TestDiff is a test whose outcome or tool sequence differs from the baseline.
type TestDiff struct {
	Name     string
	Location string
	Base     string
	Head     string
	Note     string
	Sequence []SeqOp
}

// SeqOp is one call of a tool-sequence diff: Kind is "keep", "add" or "del".
type SeqOp struct {
	Kind string
	Name string
}

// MutationView lists the mutants that survived.
type MutationView struct {
	Score     string
	Killed    int
	Survived  int
	Survivors []SurvivorView
	// Operators scores each operator; empty for a report from an older engine.
	Operators []OperatorView
	// SkippedSpecs and NoApplicable are the populations the score leaves out.
	SkippedSpecs []SkippedView
	NoApplicable int
}

// OperatorView is one row of the per-operator table.
type OperatorView struct {
	Operator          string
	Generated         int
	Killed            int
	Survived          int
	NoApplicableSpecs int
	Score             string
}

// SkippedView counts specs skipped for one reason.
type SkippedView struct {
	Reason string
	Specs  int
}

// SurvivorView is a surviving mutant and the contract that would kill it.
type SurvivorView struct {
	ID          string
	Fixture     string
	Operator    string
	Description string
	Contract    string
	Suggestion  string
}

// viewBuilder carries the clipping budget and counters while a View is built.
type viewBuilder struct {
	used       int
	budgetCut  bool
	stringsCut int
	stepCount  int
	stepsCut   bool
	noticesCut int
	notices    []string
}

// text bounds one report string: cut at viewMaxString bytes on a rune
// boundary, and replaced once the page's text budget is spent.
func (b *viewBuilder) text(s string) string { return b.textTotal(s, len(s)) }

// textTotal is text for a string whose caller already cut it to a prefix of a
// value total bytes long, so a huge value is never copied whole.
func (b *viewBuilder) textTotal(s string, total int) string {
	if b.used >= viewMaxTotalBytes {
		b.budgetCut = true
		return "[omitted: page size limit]"
	}
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) > viewMaxString || total > len(s) {
		cut := min(viewMaxString, len(s))
		for cut > 0 && cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut--
		}
		b.stringsCut++
		s = fmt.Sprintf("%s … [truncated, %d bytes total]", s[:cut], total)
	}
	b.used += len(s)
	return s
}

// notef records a page notice. A notice is cut to viewMaxNoticeBytes, counts
// against the text budget, and past viewMaxNotices only a count is kept.
func (b *viewBuilder) notef(format string, args ...any) {
	if len(b.notices) >= viewMaxNotices {
		b.noticesCut++
		return
	}
	msg := fmt.Sprintf(format, args...)
	if len(msg) > viewMaxNoticeBytes {
		msg = strings.ToValidUTF8(msg[:viewMaxNoticeBytes], "") + "…"
	}
	b.used += len(msg)
	b.notices = append(b.notices, msg)
}

// inputBudget bounds the bytes read across every input file of one run.
type inputBudget struct{ left int64 }

// readLimited reads path, refusing a file that exceeds what is left of the
// run's input budget.
func (in *inputBudget) readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, in.left+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > in.left {
		return nil, fmt.Errorf("%s exceeds the %d MiB input limit for all files together", path, viewMaxInputBytes>>20)
	}
	in.left -= int64(len(data))
	return data, nil
}

// loadViewReports reads and merges report-v2 files with the viewer's size limit.
func loadViewReports(in *inputBudget, paths []string) (*CIReport, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one report is required")
	}
	reports := make([]*CIReport, 0, len(paths))
	for _, p := range paths {
		data, err := in.readLimited(p)
		if err != nil {
			return nil, err
		}
		var r CIReport
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("decode report %s: %w", p, err)
		}
		if r.ReportVersion != 2 {
			return nil, fmt.Errorf("report %s is version %d; expected 2", p, r.ReportVersion)
		}
		reports = append(reports, &r)
	}
	return MergeCI(reports), nil
}

// loadMutation reads a `mutate --json` report.
func loadMutation(in *inputBudget, path string) (*mutate.Report, error) {
	data, err := in.readLimited(path)
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("decode mutation report %s: %w", path, err)
	}
	if _, ok := probe["mutants"]; !ok {
		return nil, fmt.Errorf("%s is not a mutate report: no \"mutants\" key", path)
	}
	var r mutate.Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decode mutation report %s: %w", path, err)
	}
	return &r, nil
}

// BuildView loads the inputs and builds the page model.
func BuildView(opts ViewOptions) (*View, error) {
	in := &inputBudget{left: viewMaxInputBytes}
	head, err := loadViewReports(in, opts.Reports)
	if err != nil {
		return nil, err
	}
	b := &viewBuilder{}
	v := &View{}
	v.Summary = b.summary(head)
	b.tests(v, head)
	v.TracesMissing = len(head.Traces) == 0
	if len(opts.Baselines) > 0 {
		base, err := loadViewReports(in, opts.Baselines)
		if err != nil {
			return nil, err
		}
		v.Baseline = b.baseline(base, head)
	}
	if opts.Mutation != "" {
		m, err := loadMutation(in, opts.Mutation)
		if err != nil {
			return nil, err
		}
		v.Mutation = b.mutation(m)
	}
	if b.stepsCut {
		b.notef("trace trees were cut at %d steps per trace, %d per page and depth %d; omitted ranges are marked", viewMaxStepsPerTrace, viewMaxSteps, viewMaxDepth)
	}
	if b.budgetCut {
		b.notef("the page reached its %d MiB text limit; later fields are marked omitted", viewMaxTotalBytes>>20)
	}
	if b.stringsCut > 0 {
		b.notef("%d field(s) longer than %d bytes were truncated", b.stringsCut, viewMaxString)
	}
	if b.noticesCut > 0 {
		b.notices = append(b.notices, fmt.Sprintf("%d more notice(s) omitted", b.noticesCut))
	}
	v.Notices = b.notices
	return v, nil
}

func (b *viewBuilder) summary(r *CIReport) SummaryView {
	skips := notEvaluatedSkips(r)
	s := SummaryView{Tests: len(r.Tests), Simulated: r.Simulated}
	reasons := map[string]bool{}
	for _, t := range r.Tests {
		switch class, _ := classify(t, skips); class {
		case classPass:
			s.Passed++
		case classFail:
			s.Failed++
		case classNE:
			s.NotEvaluated++
			for _, reason := range t.NotEvaluatedReasons {
				reasons[reason] = true
			}
		default:
			s.Skipped++
		}
	}
	for reason := range reasons {
		s.NotEvaluatedBy = append(s.NotEvaluatedBy, b.text(reason))
	}
	sort.Strings(s.NotEvaluatedBy)
	if len(s.NotEvaluatedBy) > viewMaxListed {
		b.notef("%d more not-evaluated reason(s) not shown", len(s.NotEvaluatedBy)-viewMaxListed)
		s.NotEvaluatedBy = s.NotEvaluatedBy[:viewMaxListed]
	}
	sum := r.Summary
	s.Assertions = fmt.Sprintf("%d total: %d pass, %d soft fail, %d hard fail, %d not evaluated",
		sum.Total, sum.Passed, sum.SoftFail, sum.HardFail, sum.NotEvaluated)
	s.PassRate = passRate(sum)
	if sum.Replay != nil {
		s.Replay = fmt.Sprintf("%d replayed, %d live, %d miss(es); live cost %s",
			sum.Replay.Replayed, sum.Replay.Live, sum.Replay.Misses, usd(sum.Replay.LiveCostUSD))
	} else {
		s.Replay = "no replay session recorded"
	}
	s.Cost = usd(r.TotalCost)
	s.Duration = fmt.Sprintf("%d ms", r.TotalDurationMS)
	return s
}

// passRate is passed / (passed + failed); not_evaluated stays out of the
// denominator (contract C1).
func passRate(s CISummary) string {
	den := s.Passed + s.SoftFail + s.HardFail
	if den == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(s.Passed)/float64(den))
}

func usd(v float64) string { return fmt.Sprintf("$%.4f", v) }

// classify maps a test row to a display class and label.
func classify(t CITest, skips map[testKey]bool) (class, label string) {
	switch t.Outcome {
	case OutcomePassed:
		return classPass, "pass"
	case OutcomeFailed:
		return classFail, "fail"
	}
	if skips[testKey{t.src, t.TestID}] {
		if len(t.NotEvaluatedReasons) > 0 {
			return classNE, "not evaluated (" + strings.Join(t.NotEvaluatedReasons, ", ") + ")"
		}
		return classNE, "not evaluated"
	}
	return classSkip, "skipped"
}

func provenance(t CITest) string {
	r := t.Replay
	if r == nil {
		return "live: no replay session"
	}
	p := fmt.Sprintf("replay (%s) from %s: %d replayed, %d live, %d miss(es)",
		r.Mode, strings.Join(r.Fixtures, ", "), r.Replayed, r.Live, r.Misses)
	if r.Miss != "" {
		p += "; " + r.Miss
	}
	return p
}

func statusClass(status string) string {
	switch status {
	case types.StatusPass:
		return classPass
	case types.StatusSoftFail, types.StatusHardFail:
		return classFail
	default:
		return classNE
	}
}

func (b *viewBuilder) tests(v *View, r *CIReport) {
	skips := notEvaluatedSkips(r)
	rows := map[testKey][]CIResult{}
	var orphan []CIResult
	known := map[testKey]bool{}
	for _, t := range r.Tests {
		known[testKey{t.src, t.TestID}] = true
	}
	for _, res := range r.Results {
		k := testKey{res.src, res.TestID}
		if res.TestID == "" || !known[k] {
			orphan = append(orphan, res)
			continue
		}
		rows[k] = append(rows[k], res)
	}
	traces := &traceDecoder{raw: r.Traces, decoded: map[string]*types.Trace{}}
	findings := 0
	shown := min(len(r.Tests), viewMaxTests)
	if len(r.Tests) > shown {
		b.notef("%d more test(s) not rendered (limit %d)", len(r.Tests)-shown, viewMaxTests)
	}
	v.Failures = v.Summary.Failed
	views := make([]TestView, shown)
	// Tests that did not pass render first, so their content gets the page's
	// text budget before passing tests can spend it.
	for _, passing := range []bool{false, true} {
		for i := 0; i < shown; i++ {
			t := r.Tests[i]
			class, label := classify(t, skips)
			if (class == classPass) != passing {
				continue
			}
			tv := TestView{
				Anchor:     fmt.Sprintf("t%d", i),
				Name:       b.text(t.Name),
				Location:   b.text(fmt.Sprintf("%s:%d", t.File, t.Line)),
				Class:      class,
				Label:      b.text(label),
				Duration:   fmt.Sprintf("%d ms", t.DurationMS),
				Provenance: b.text(provenance(t)),
				Open:       class != classPass,
			}
			b.fillTest(&tv, rows[testKey{t.src, t.TestID}], traces, &findings)
			v.TraceCount += len(tv.Traces)
			views[i] = tv
		}
	}
	v.Tests = views
	if len(orphan) > 0 {
		b.notef("%d assertion row(s) carry no test context and are not listed", len(orphan))
	}
	for _, n := range traces.notices {
		b.notef("%s", n)
	}
}

// fillTest adds a test's findings, passed ids and traces, and highlights the
// step each failing row points at.
func (b *viewBuilder) fillTest(tv *TestView, rows []CIResult, traces *traceDecoder, findings *int) {
	// Traces behind failing rows come first, so the cap never drops the failing one.
	var traceIDs []string
	seen := map[string]bool{}
	for _, failing := range []bool{true, false} {
		for _, res := range rows {
			if res.TraceID != "" && !seen[res.TraceID] && (!failing || isFailure(res.Status)) {
				seen[res.TraceID] = true
				traceIDs = append(traceIDs, res.TraceID)
			}
		}
	}
	must := map[string]map[int]bool{}
	nonPass := 0
	for _, res := range rows {
		if res.Status == types.StatusPass {
			continue
		}
		if nonPass++; nonPass > viewMaxFindingRows { // the findings rendered below
			break
		}
		if idx, ok := stepIndex(res.TraceNodePath); ok && isFailure(res.Status) {
			if must[res.TraceID] == nil {
				must[res.TraceID] = map[int]bool{}
			}
			must[res.TraceID][idx] = true
		}
	}
	byID := map[string]int{}
	for _, id := range traceIDs {
		if len(tv.Traces) >= viewMaxTracesPerTest {
			b.notef("test %s: %d more trace(s) not rendered", clipForNotice(tv.Name), len(traceIDs)-viewMaxTracesPerTest)
			break
		}
		tr := traces.get(id)
		if tr == nil {
			continue
		}
		byID[id] = len(tv.Traces)
		tv.Traces = append(tv.Traces, b.traceView(fmt.Sprintf("%s-tr%d", tv.Anchor, len(tv.Traces)), id, tr, must[id]))
	}
	for _, res := range rows {
		if res.Status == types.StatusPass {
			if len(tv.PassedIDs) < viewMaxFindingRows {
				tv.PassedIDs = append(tv.PassedIDs, b.text(res.AssertionID))
			} else {
				tv.RowsCut++
			}
			continue
		}
		if len(tv.Findings) >= viewMaxFindingRows {
			tv.RowsCut++
			continue
		}
		f := b.finding(res, tv.Provenance, *findings)
		*findings++
		if ti, ok := byID[res.TraceID]; ok && isFailure(res.Status) {
			f.StepAnchor = markStep(&tv.Traces[ti], res, f)
		}
		tv.Findings = append(tv.Findings, f)
	}
}

func isFailure(status string) bool {
	return status == types.StatusHardFail || status == types.StatusSoftFail
}

func (b *viewBuilder) finding(res CIResult, prov string, n int) FindingView {
	f := FindingView{
		Anchor:      fmt.Sprintf("a%d", n),
		AssertionID: b.text(res.AssertionID),
		Type:        b.text(res.Type),
		Status:      b.text(res.Status),
		Class:       statusClass(res.Status),
		Reason:      b.text(res.Reason),
		Explanation: b.text(res.Explanation),
		Expected:    b.text(res.Expected),
		Actual:      b.text(res.Actual),
		Path:        b.text(res.TraceNodePath),
		Cost:        usd(res.Cost),
		Provenance:  prov,
	}
	return f
}

// stepIndex parses the trace_node_path grammar the engine writes for a step:
// `steps[N]` (optionally followed by `.field`), N the index in the trace's
// top-level steps. Other paths name no step.
func stepIndex(path string) (int, bool) {
	rest, ok := strings.CutPrefix(path, "steps[")
	if !ok {
		return 0, false
	}
	digits, tail, ok := strings.Cut(rest, "]")
	if !ok || digits == "" || len(digits) > 9 || (tail != "" && tail[0] != '.') {
		return 0, false
	}
	n := 0
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// markStep highlights the step res points at and returns its anchor ("" when
// the path names no rendered step).
func markStep(tr *TraceView, res CIResult, f FindingView) string {
	idx, ok := stepIndex(res.TraceNodePath)
	if !ok {
		return ""
	}
	for i := range tr.Steps {
		if tr.Steps[i].Index == idx && tr.Steps[i].Omitted == 0 {
			tr.Steps[i].Highlight = true
			tr.Steps[i].Marks = append(tr.Steps[i].Marks, MarkView{Anchor: f.Anchor, Label: f.AssertionID})
			return tr.Steps[i].Anchor
		}
	}
	return ""
}

// traceDecoder decodes each embedded trace once.
type traceDecoder struct {
	raw     map[string]json.RawMessage
	decoded map[string]*types.Trace
	notices []string
}

func (d *traceDecoder) get(id string) *types.Trace {
	if t, ok := d.decoded[id]; ok {
		return t
	}
	raw, ok := d.raw[id]
	if !ok {
		d.decoded[id] = nil
		return nil
	}
	var t types.Trace
	if err := json.Unmarshal(raw, &t); err != nil {
		d.notices = append(d.notices, fmt.Sprintf("trace %q is not a valid trace: %v", clipForNotice(id), clipForNotice(err.Error())))
		d.decoded[id] = nil
		return nil
	}
	d.decoded[id] = &t
	return &t
}

func clipForNotice(s string) string {
	if len(s) > 80 {
		return strings.ToValidUTF8(s[:80], "") + "…"
	}
	return s
}

func (b *viewBuilder) traceView(anchor, id string, t *types.Trace, must map[int]bool) TraceView {
	tv := TraceView{
		Anchor:  anchor,
		ID:      b.text(t.TraceID),
		AgentID: b.text(t.AgentID),
		Input:   b.jsonText(t.Input),
		Output:  b.jsonText(t.Output),
	}
	if tv.ID == "" {
		tv.ID = b.text(id)
	}
	tv.Sequence = b.text(strings.Join(toolSequence(t, viewMaxSequence), " → "))
	tv.Steps = b.steps(anchor, t, 0, must)
	return tv
}

// steps renders t.Steps. Past the per-trace or page limit a run of steps
// collapses into one "omitted" placeholder, except steps in must.
func (b *viewBuilder) steps(prefix string, t *types.Trace, depth int, must map[int]bool) []StepView {
	var out []StepView
	skipped := 0
	flush := func(at int) {
		if skipped > 0 {
			out = append(out, StepView{Anchor: fmt.Sprintf("%s-o%d", prefix, at), Type: "omitted", Omitted: skipped})
			skipped = 0
		}
	}
	for i := range t.Steps {
		if (len(out) >= viewMaxStepsPerTrace || b.stepCount >= viewMaxSteps || depth >= viewMaxDepth) && !must[i] {
			skipped++
			b.stepsCut = true
			continue
		}
		flush(i)
		out = append(out, b.stepView(prefix, i, &t.Steps[i], depth, must))
	}
	flush(len(t.Steps))
	return out
}

func (b *viewBuilder) stepView(prefix string, i int, s *types.Step, depth int, must map[int]bool) StepView {
	b.stepCount++
	sv := StepView{
		Anchor: fmt.Sprintf("%s-s%d", prefix, i),
		Index:  i,
		Type:   b.text(s.Type),
		Name:   b.text(s.Name),
		Args:   b.jsonText(s.Args),
		Result: b.jsonText(s.Result),
	}
	if depth == 0 {
		sv.Path = fmt.Sprintf("steps[%d]", i)
	}
	if s.Error != nil {
		sv.Error = b.text(s.Error.Type + ": " + s.Error.Message)
	}
	if s.SubTrace != nil {
		sv.Children = b.steps(sv.Anchor, s.SubTrace, depth+1, nil)
	}
	return sv
}

// jsonText renders a raw JSON value as bounded text. A value far over the
// per-string cap is cut before indenting, so a huge blob never costs more than
// the cap.
func (b *viewBuilder) jsonText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if b.used >= viewMaxTotalBytes {
		b.budgetCut = true
		return "[omitted: page size limit]"
	}
	if len(raw) > viewMaxString {
		// Compact text, cut before it is copied.
		return b.textTotal(string(raw[:min(len(raw), viewMaxString+utf8.UTFMax)]), len(raw))
	}
	if jsonDepthOver(raw, viewMaxIndentDepth) {
		return b.text(string(raw))
	}
	return b.text(prettyJSON(raw))
}

// jsonDepthOver reports whether raw nests arrays or objects deeper than max.
// json.Indent is quadratic in nesting depth, so deep values stay compact.
func jsonDepthOver(raw []byte, max int) bool {
	depth, inString, escaped := 0, false, false
	for _, c := range raw {
		switch {
		case inString && escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
		case c == '[' || c == '{':
			if depth++; depth > max {
				return true
			}
		case c == ']' || c == '}':
			depth--
		}
	}
	return false
}

// prettyJSON indents a JSON value for display; raw is already valid JSON
// because the report decoded it.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// toolSequence lists the tool-call names of t depth-first, at most max.
func toolSequence(t *types.Trace, max int) []string {
	var out []string
	var walk func(*types.Trace)
	walk = func(tr *types.Trace) {
		for i := range tr.Steps {
			if len(out) >= max {
				return
			}
			s := &tr.Steps[i]
			if s.Type == types.StepTypeToolCall {
				out = append(out, s.Name)
			}
			if s.SubTrace != nil {
				walk(s.SubTrace)
			}
		}
	}
	walk(t)
	return out
}

func (b *viewBuilder) mutation(m *mutate.Report) *MutationView {
	killed, survived := m.Counts()
	mv := &MutationView{Killed: killed, Survived: survived, Score: "n/a"}
	if m.Score != nil {
		mv.Score = fmt.Sprintf("%.1f%%", 100**m.Score)
	}
	mv.NoApplicable = m.Excluded.MutantsNoApplicableSpecs
	for _, st := range m.Operators {
		if len(mv.Operators) >= viewMaxListed {
			b.notef("%d operator row(s) in total; the first %d are shown", len(m.Operators), viewMaxListed)
			break
		}
		ov := OperatorView{
			Operator: b.text(string(st.Operator)), Generated: st.Generated, Killed: st.Killed,
			Survived: st.Survived, NoApplicableSpecs: st.NoApplicableSpecs, Score: "n/a",
		}
		if st.Score != nil {
			ov.Score = fmt.Sprintf("%.1f%%", 100**st.Score)
		}
		mv.Operators = append(mv.Operators, ov)
	}
	for _, sk := range m.Excluded.SkippedSpecs {
		if len(mv.SkippedSpecs) >= viewMaxListed {
			b.notef("%d skipped-spec reason(s) in total; the first %d are shown", len(m.Excluded.SkippedSpecs), viewMaxListed)
			break
		}
		mv.SkippedSpecs = append(mv.SkippedSpecs, SkippedView{Reason: b.text(sk.Reason), Specs: sk.Specs})
	}
	suggestions := map[string]mutate.Suggestion{}
	for _, s := range m.SurvivorsSuggestions {
		if _, dup := suggestions[s.MutantID]; !dup {
			suggestions[s.MutantID] = s
		}
	}
	for _, mr := range m.Mutants {
		if mr.Status != mutate.StatusSurvived {
			continue
		}
		if len(mv.Survivors) >= viewMaxListed {
			b.notef("%d surviving mutant(s) in total; the first %d are shown", survived, viewMaxListed)
			break
		}
		sv := SurvivorView{
			ID:          b.text(mr.ID),
			Fixture:     b.text(mr.Fixture),
			Operator:    b.text(string(mr.Operator)),
			Description: b.text(mr.Description),
		}
		if s, ok := suggestions[mr.ID]; ok {
			contract, err := json.Marshal(s.Contract)
			if err == nil {
				sv.Contract = b.text(string(contract))
			}
			sv.Suggestion = b.text(s.Description)
		}
		mv.Survivors = append(mv.Survivors, sv)
	}
	return mv
}
