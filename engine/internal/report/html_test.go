package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const viewFixtures = "testdata/view"

// renderFixtures builds and renders the INC-1042 head, baseline and mutation
// fixtures; edit may rewrite the decoded head report first.
func renderFixtures(t *testing.T, edit func(head map[string]any), withBase, withMutation bool) string {
	t.Helper()
	dir := t.TempDir()
	head := writeEdited(t, dir, "head.json", edit)
	opts := ViewOptions{Reports: []string{head}}
	if withBase {
		opts.Baselines = []string{filepath.Join(viewFixtures, "base.json")}
	}
	if withMutation {
		opts.Mutation = filepath.Join(viewFixtures, "mutate.json")
	}
	return renderOpts(t, opts)
}

func renderOpts(t *testing.T, opts ViewOptions) string {
	t.Helper()
	v, err := BuildView(opts)
	if err != nil {
		t.Fatalf("BuildView: %v", err)
	}
	var buf bytes.Buffer
	if err := RenderHTML(&buf, v); err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	return buf.String()
}

func writeEdited(t *testing.T, dir, name string, edit func(map[string]any)) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(viewFixtures, "head.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(doc)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRenderHTML_Inc1042Failure_HighlightsSecondIssueRefund(t *testing.T) {
	page := renderFixtures(t, nil, true, true)

	hit := regexp.MustCompile(`<li class="step hit" id="([^"]+)">`).FindAllStringSubmatch(page, -1)
	if len(hit) != 1 {
		t.Fatalf("want exactly one highlighted step, got %d", len(hit))
	}
	if hit[0][1] != "t0-tr0-s3" {
		t.Fatalf("highlighted step id = %q, want t0-tr0-s3 (steps[3], the second issue_refund)", hit[0][1])
	}
	hitBlock := page[strings.Index(page, `id="t0-tr0-s3"`):]
	hitBlock = hitBlock[:strings.Index(hitBlock, "</li>")]
	for _, want := range []string{"steps[3]", "issue_refund", "R-2", "failing span", `href="#a0"`, "<details open>"} {
		if !strings.Contains(hitBlock, want) {
			t.Errorf("highlighted step lacks %q:\n%s", want, hitBlock)
		}
	}
	if strings.Contains(page[strings.Index(page, `id="t0-tr0-s1"`):strings.Index(page, `id="t0-tr0-s2"`)], "failing span") {
		t.Error("the first issue_refund (steps[1]) must not be marked")
	}
	for _, want := range []string{
		`id="a0"`, "issue_refund called 2×", "issue_refund &lt;= 1 per order_id", // expected
		"replay (strict) from inc-1042", "jump to the failing span", `href="#t0-tr0-s3"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestRenderHTML_BaselineDiff_ShowsPassRateAndChangedToolSequence(t *testing.T) {
	page := renderFixtures(t, nil, true, false)

	for _, want := range []string{
		"assertion pass rate", "100.0%", "66.7%", "-33.3 pp",
		"outcome changed tool sequence changed",
		`<span class="seq-add">+issue_refund</span>`,
		`&#43;0.0003 USD`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("baseline diff lacks %q", want)
		}
	}
	if strings.Contains(page, `class="seq-del"`) {
		t.Error("no call was removed in the INC-1042 head run")
	}
}

func TestRenderHTML_Mutation_ListsSurvivorWithSuggestion(t *testing.T) {
	page := renderFixtures(t, nil, false, true)

	for _, want := range []string{
		"Score 50.0%: 1 killed, 1 survived", "inc-1042:duplicate_step:1",
		"add: issue_refund at most once per order_id", "tool_cardinality",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("mutation section lacks %q", want)
		}
	}
	if strings.Contains(page, "inc-1042:drop_step:1") {
		t.Error("a killed mutant must not be listed")
	}
}

func TestRenderHTML_NoTraces_NamesTheEnvVar(t *testing.T) {
	page := renderFixtures(t, func(head map[string]any) {
		delete(head, "traces")
		for _, r := range head["results"].([]any) {
			delete(r.(map[string]any), "trace_id")
		}
	}, false, false)

	if !strings.Contains(page, "ATTEST_REPORT_TRACES=1") {
		t.Error("the notice must name ATTEST_REPORT_TRACES=1")
	}
	if strings.Contains(page, `class="step`) {
		t.Error("no trace tree without traces")
	}
}

func TestRenderHTML_SummaryClassifiesTests(t *testing.T) {
	v, err := BuildView(ViewOptions{Reports: []string{filepath.Join(viewFixtures, "head.json")}})
	if err != nil {
		t.Fatal(err)
	}

	s := v.Summary
	if s.Tests != 3 || s.Passed != 1 || s.Failed != 1 || s.NotEvaluated != 1 || s.Skipped != 0 {
		t.Fatalf("summary = %+v", s)
	}
	if got := strings.Join(s.NotEvaluatedBy, ","); got != "provider_not_configured" {
		t.Errorf("not evaluated reasons = %q", got)
	}
	if v.Failures != 1 || v.TraceCount != 2 {
		t.Errorf("failures=%d traces=%d, want 1 and 2", v.Failures, v.TraceCount)
	}
}

// payloads are the strings an attacker controlling a trace or report would plant.
var payloads = []string{
	`<script>alert(1)</script>`,
	`"><img src=x onerror=alert(1)>`,
	`javascript:alert(1)`,
	`</style><b onload=alert(1)>`,
	`</details></li></ul><iframe src=//evil.example/x>`,
}

func plant(p string) string { return "P:" + p }

func TestRenderHTML_PlantedPayloads_AreEscapedEverywhere(t *testing.T) {
	for i, p := range payloads {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			planted := plant(p)
			page := renderFixtures(t, func(head map[string]any) {
				plantInReport(head, planted)
			}, true, false)

			escaped := escapedForm(planted)
			if escaped != planted && strings.Contains(page, planted) {
				t.Fatalf("raw payload %q appears in the page", planted)
			}
			if !strings.Contains(page, escaped) {
				t.Fatalf("escaped payload %q missing from the page", escaped)
			}
			assertPageIsInert(t, page)
		})
	}
}

// plantInReport writes payload into every report field the page renders.
func plantInReport(head map[string]any, payload string) {
	tests := head["tests"].([]any)
	for _, tt := range tests {
		m := tt.(map[string]any)
		m["name"] = payload
		m["file"] = "tests/" + payload
	}
	tests[0].(map[string]any)["replay"].(map[string]any)["miss"] = payload
	tests[0].(map[string]any)["replay"].(map[string]any)["fixtures"] = []any{payload}
	for _, r := range head["results"].([]any) {
		m := r.(map[string]any)
		m["explanation"] = payload
		m["expected"] = payload
		m["actual"] = payload
		m["type"] = payload
		if m["status"] == "hard_fail" {
			m["assertion_id"] = payload
		}
	}
	traces := head["traces"].(map[string]any)
	tr := traces["trc_inc_1042"].(map[string]any)
	tr["agent_id"] = payload
	tr["input"] = map[string]any{"message": payload}
	tr["output"] = map[string]any{"message": payload}
	for _, s := range tr["steps"].([]any) {
		st := s.(map[string]any)
		st["name"] = payload
		st["args"] = map[string]any{payload: payload}
		st["result"] = map[string]any{"text": payload}
		st["type"] = "tool_call"
		st["error"] = map[string]any{"type": payload, "message": payload}
	}
}

// escapedForm is how html/template renders a planted payload in HTML text.
func escapedForm(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}

var (
	attrURL  = regexp.MustCompile(`(?i)\b(src|href|action|formaction|poster|data|srcset|xlink:href)\s*=\s*"([^"]*)"`)
	anyURL   = regexp.MustCompile(`(?i)https?://`)
	cssFetch = regexp.MustCompile(`(?i)url\(|@import`)
)

// assertPageIsInert checks the page's structural guarantees: no script, one
// style element, the exact CSP, and links only to in-page anchors.
func assertPageIsInert(t *testing.T, page string) {
	t.Helper()
	lower := strings.ToLower(page)
	if strings.Contains(lower, "<script") {
		t.Error("page contains a <script element")
	}
	if n := strings.Count(lower, "<style"); n != 1 {
		t.Errorf("page has %d <style elements, want exactly 1", n)
	}
	if n := strings.Count(lower, "</style>"); n != 1 {
		t.Errorf("page has %d </style> tags, want exactly 1", n)
	}
	const csp = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">`
	if n := strings.Count(page, "Content-Security-Policy"); n != 1 || !strings.Contains(page, csp) {
		t.Errorf("want exactly the CSP meta %s (found %d mentions)", csp, n)
	}
	for _, m := range attrURL.FindAllStringSubmatch(page, -1) {
		if !strings.HasPrefix(m[2], "#") {
			t.Errorf("attribute %s points outside the page: %q", m[1], m[2])
		}
	}
	if anyURL.MatchString(page) {
		t.Errorf("page contains an http(s) URL: %q", anyURL.FindString(page))
	}
	if cssFetch.MatchString(page) {
		t.Error("page contains url( or @import")
	}
	for _, tag := range []string{"<iframe", "<img", "<link", "<object", "<embed", "<form", "<base"} {
		if strings.Contains(lower, tag) {
			t.Errorf("page contains %s", tag)
		}
	}
	if regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`).MatchString(page) {
		t.Error("page contains an inline event handler attribute")
	}
}

func TestRenderHTML_CleanPage_IsInert(t *testing.T) {
	assertPageIsInert(t, renderFixtures(t, nil, true, true))
}

func TestRenderHTML_PlantedAnchorIDs_AreIndexDerived(t *testing.T) {
	page := renderFixtures(t, func(head map[string]any) {
		plantInReport(head, `"><b id=x>`)
	}, true, true)

	for _, m := range regexp.MustCompile(`\bid="([^"]*)"`).FindAllStringSubmatch(page, -1) {
		if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(m[1]) {
			t.Errorf("id %q is not index-derived", m[1])
		}
	}
}

func TestRenderHTML_OversizedField_IsTruncatedWithMarker(t *testing.T) {
	huge := strings.Repeat("A", 100<<10)
	page := renderFixtures(t, func(head map[string]any) {
		tr := head["traces"].(map[string]any)["trc_inc_1042"].(map[string]any)
		tr["steps"].([]any)[0].(map[string]any)["result"] = map[string]any{"blob": huge}
		head["results"].([]any)[1].(map[string]any)["explanation"] = huge
	}, false, false)

	if strings.Contains(page, strings.Repeat("A", viewMaxString+1)) {
		t.Fatal("a field longer than the cap was rendered whole")
	}
	if !strings.Contains(page, "truncated, ") {
		t.Error("truncated fields must carry a marker")
	}
	if !strings.Contains(page, "longer than 4096 bytes were truncated") {
		t.Error("the page must say fields were truncated")
	}
	if len(page) > 1<<20 {
		t.Errorf("page is %d bytes for two oversized fields", len(page))
	}
}

func TestRenderHTML_TruncationKeepsUTF8Valid(t *testing.T) {
	multi := strings.Repeat("é", viewMaxString)
	b := &viewBuilder{}

	got := b.text(multi)

	if !strings.Contains(got, "truncated") {
		t.Fatal("expected truncation")
	}
	if strings.ContainsRune(strings.SplitN(got, " … ", 2)[0], '\uFFFD') {
		t.Error("cut inside a rune")
	}
}

func TestRenderHTML_ManySteps_AreCappedButFailingStepStaysVisible(t *testing.T) {
	page := renderFixtures(t, func(head map[string]any) {
		tr := head["traces"].(map[string]any)["trc_inc_1042"].(map[string]any)
		var steps []any
		for i := 0; i < viewMaxStepsPerTrace+300; i++ {
			steps = append(steps, map[string]any{"type": "tool_call", "name": fmt.Sprintf("tool_%d", i), "args": map[string]any{}, "result": map[string]any{}})
		}
		tr["steps"] = steps
		for _, r := range head["results"].([]any) {
			m := r.(map[string]any)
			if m["status"] == "hard_fail" {
				m["trace_node_path"] = fmt.Sprintf("steps[%d]", viewMaxStepsPerTrace+250)
			}
		}
	}, false, false)

	if !strings.Contains(page, "step(s) omitted") || !strings.Contains(page, "trace trees were cut") {
		t.Error("omitted ranges and the cut notice must be shown")
	}
	hit := fmt.Sprintf(`<li class="step hit" id="t0-tr0-s%d">`, viewMaxStepsPerTrace+250)
	if !strings.Contains(page, hit) {
		t.Errorf("the failing step beyond the cap must still render highlighted (%s)", hit)
	}
	if strings.Contains(page, fmt.Sprintf("tool_%d<", viewMaxStepsPerTrace+10)) {
		t.Error("a step beyond the cap was rendered")
	}
}

func TestStepIndex_Grammar(t *testing.T) {
	cases := []struct {
		path string
		want int
		ok   bool
	}{
		{"steps[3]", 3, true},
		{"steps[0]", 0, true},
		{"steps[12].result", 12, true},
		{"steps[3]x", 0, false},
		{"steps[]", 0, false},
		{"steps[-1]", 0, false},
		{"steps[1234567890]", 0, false},
		{"trace.tool_cardinality", 0, false},
		{"output.message", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := stepIndex(tc.path)
		if got != tc.want || ok != tc.ok {
			t.Errorf("stepIndex(%q) = %d, %v; want %d, %v", tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDiffSequence_MarksAddedAndRemovedCalls(t *testing.T) {
	ops := diffSequence([]string{"a", "b", "c"}, []string{"a", "b", "b", "d"})

	var got []string
	for _, op := range ops {
		got = append(got, op.Kind+":"+op.Name)
	}
	want := "keep:a keep:b del:c add:b add:d"
	if strings.Join(got, " ") != want {
		t.Errorf("diff = %s, want %s", strings.Join(got, " "), want)
	}
}

func TestBuildView_InputErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{"), 0o644)
	v1 := filepath.Join(dir, "v1.json")
	_ = os.WriteFile(v1, []byte(`{"report_version":1}`), 0o644)
	notMutate := filepath.Join(viewFixtures, "head.json")
	cases := map[string]ViewOptions{
		"no reports":        {},
		"missing report":    {Reports: []string{filepath.Join(dir, "none.json")}},
		"malformed":         {Reports: []string{bad}},
		"wrong version":     {Reports: []string{v1}},
		"not a mutation":    {Reports: []string{notMutate}, Mutation: notMutate},
		"missing baseline":  {Reports: []string{notMutate}, Baselines: []string{filepath.Join(dir, "none.json")}},
		"missing mutations": {Reports: []string{notMutate}, Mutation: filepath.Join(dir, "none.json")},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildView(opts); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestBuildView_MultipleReports_KeepTraceIDsApart(t *testing.T) {
	a := filepath.Join(viewFixtures, "head.json")
	dir := t.TempDir()
	b := writeEdited(t, dir, "other.json", func(head map[string]any) {
		head["traces"].(map[string]any)["trc_inc_1042"].(map[string]any)["agent_id"] = "second-report-agent"
	})

	page := renderOpts(t, ViewOptions{Reports: []string{a, b}})

	if !strings.Contains(page, "second-report-agent") || !strings.Contains(page, "refund-desk") {
		t.Error("each report's trace must render with its own content")
	}
}

func TestRenderHTML_FailingTraceBeyondFiveTraces_IsStillRendered(t *testing.T) {
	page := renderFixtures(t, func(head map[string]any) {
		traces := head["traces"].(map[string]any)
		rows := head["results"].([]any)
		var extra []any
		for i := 0; i < 7; i++ {
			id := fmt.Sprintf("trial_%d", i)
			traces[id] = map[string]any{"schema_version": 2, "trace_id": id, "agent_id": "a", "output": map[string]any{}, "steps": []any{}}
			extra = append(extra, map[string]any{"assertion_id": "pad_" + id, "status": "pass", "test_id": "tests/test_refund.py::test_inc_1042", "file": "tests/test_refund.py", "line": 12, "trace_id": id})
		}
		// The padding rows come first; the failing row's trace is last.
		head["results"] = append(extra, rows...)
	}, false, false)

	if !strings.Contains(page, `<li class="step hit" id="t0-tr0-s3">`) {
		t.Fatal("the failing trace must be rendered first and its step highlighted")
	}
}

func TestBuildView_FailureCountCoversTestsPastTheRenderLimit(t *testing.T) {
	dir := t.TempDir()
	path := writeEdited(t, dir, "many.json", func(head map[string]any) {
		var tests []any
		for i := 0; i < viewMaxTests+50; i++ {
			outcome := "passed"
			if i >= viewMaxTests {
				outcome = "failed"
			}
			tests = append(tests, map[string]any{"test_id": fmt.Sprint("t", i), "name": fmt.Sprint("t", i), "file": "a.py", "line": 1, "outcome": outcome})
		}
		head["tests"] = tests
		head["results"] = []any{}
	})

	v, err := BuildView(ViewOptions{Reports: []string{path}})
	if err != nil {
		t.Fatal(err)
	}

	if v.Failures != 50 || len(v.Tests) != viewMaxTests {
		t.Fatalf("failures=%d rendered=%d, want 50 and %d", v.Failures, len(v.Tests), viewMaxTests)
	}
}

func TestRenderHTML_PassingTestsCannotSpendTheBudgetOfAFailingTest(t *testing.T) {
	page := renderFixtures(t, func(head map[string]any) {
		traces := head["traces"].(map[string]any)
		blob := strings.Repeat("x", 3000)
		var tests, rows []any
		for i := 0; i < 40; i++ { // 40 passing tests of ~600 KiB of trace text
			id := fmt.Sprint("pass_", i)
			var steps []any
			for s := 0; s < 50; s++ {
				steps = append(steps, map[string]any{"type": "tool_call", "name": "n", "args": map[string]any{"a": blob}, "result": map[string]any{"r": blob}})
			}
			traces[id] = map[string]any{"schema_version": 2, "trace_id": id, "agent_id": "a", "output": map[string]any{}, "steps": steps}
			tid := fmt.Sprint("p::", i)
			tests = append(tests, map[string]any{"test_id": tid, "name": tid, "file": "a.py", "line": 1, "outcome": "passed"})
			rows = append(rows, map[string]any{"assertion_id": "ok", "status": "pass", "test_id": tid, "file": "a.py", "line": 1, "trace_id": id})
		}
		fail := head["tests"].([]any)[0]
		head["tests"] = append(tests, fail)
		for _, r := range head["results"].([]any) {
			if r.(map[string]any)["test_id"] == "tests/test_refund.py::test_inc_1042" {
				rows = append(rows, r)
			}
		}
		head["results"] = rows
	}, false, false)

	if !strings.Contains(page, "page size limit") { // 40*50*6 KB = 12 MiB exceeds the 8 MiB budget
		t.Fatal("test setup must exhaust the text budget")
	}
	if !strings.Contains(page, "issue_refund called 2×") || !strings.Contains(page, `<li class="step hit"`) {
		t.Fatal("the failing test must render in full")
	}
}

func TestBuildView_BaselineLatency_IgnoresSkippedTests(t *testing.T) {
	head := &CIReport{Tests: []CITest{{DurationMS: 100}, {DurationMS: 0}, {DurationMS: 0}}}

	if got := percentile(sortedDurations(head), 50); got != 100 {
		t.Fatalf("p50 = %d, want 100 (skipped tests have no duration)", got)
	}
}

func TestBuildView_TwoBaselines_Merge(t *testing.T) {
	base := filepath.Join(viewFixtures, "base.json")
	v, err := BuildView(ViewOptions{Reports: []string{filepath.Join(viewFixtures, "head.json")}, Baselines: []string{base, base}})
	if err != nil {
		t.Fatal(err)
	}
	var rate string
	for _, r := range v.Baseline.Rows {
		if r.Metric == "assertions failed" {
			rate = r.Base
		}
	}
	if rate != "0" || v.Baseline == nil {
		t.Fatalf("merged baseline rows = %+v", v.Baseline.Rows)
	}
}
