package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/attest-ai/attest/engine/internal/assets"
	attestmcp "github.com/attest-ai/attest/engine/internal/mcp"
	"github.com/attest-ai/attest/engine/internal/pathguard"
)

const (
	incidentFixture = "../../../protocol-tests/fixture-v1/valid/001-command-capture-expect-fail.attest.json"
	worldFixture    = "../../../protocol-tests/fixture-v1/valid/005-world-snapshot.attest.json"
	mixedReport     = "../report/testdata/ci/mixed.json"
)

// project builds a project root holding the incident and world fixtures and
// a report, and returns it with a separate directory outside the root.
func project(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "project")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "fixtures"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyFile(t, incidentFixture, filepath.Join(root, "fixtures", "incident.attest.json"))
	copyFile(t, worldFixture, filepath.Join(root, "fixtures", "world.attest.json"))
	copyFile(t, mixedReport, filepath.Join(root, "report.json"))
	return root, outside
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// connect starts a server on root and returns a client session on it.
func connect(t *testing.T, root string) *mcp.ClientSession {
	t.Helper()
	r, err := pathguard.NewRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	srv := attestmcp.NewServer(r, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

type callResult struct {
	IsError bool
	Text    string
	Doc     map[string]any
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) callResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s: %d content blocks, want 1", tool, len(res.Content))
	}
	out := callResult{IsError: res.IsError, Text: res.Content[0].(*mcp.TextContent).Text}
	if !res.IsError {
		if err := json.Unmarshal([]byte(out.Text), &out.Doc); err != nil {
			t.Fatalf("%s: result is not a JSON object: %v", tool, err)
		}
	}
	return out
}

func listTools(t *testing.T, cs *mcp.ClientSession) []byte {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(res.Tools)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestToolsList_IdenticalAcrossCallsAndSessions(t *testing.T) {
	root, _ := project(t)
	a, b := connect(t, root), connect(t, root)

	first := listTools(t, a)
	for i, got := range [][]byte{listTools(t, a), listTools(t, b), listTools(t, b)} {
		if !bytes.Equal(first, got) {
			t.Errorf("tools/list #%d differs from the first call:\n%s\n%s", i+2, first, got)
		}
	}

	var listed []struct{ Name string }
	if err := json.Unmarshal(first, &listed); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range listed {
		names = append(names, l.Name)
	}
	want := []string{"draft_contract", "evaluate", "explain_failure", "fixture_info", "run_contract", "trace_check"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("tools %v, want %v", names, want)
	}
}

func TestToolsList_IndependentOfProjectContents(t *testing.T) {
	empty := t.TempDir()
	full, _ := project(t)
	if a, b := listTools(t, connect(t, empty)), listTools(t, connect(t, full)); !bytes.Equal(a, b) {
		t.Error("tools/list depends on the project directory")
	}
}

func TestTools_StaticTableMatchesListedTools(t *testing.T) {
	root, _ := project(t)
	res, err := connect(t, root).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	static := map[string]attestmcp.ToolInfo{}
	for _, ti := range attestmcp.Tools() {
		static[ti.Name] = ti
	}
	if len(static) != len(res.Tools) {
		t.Fatalf("static table has %d tools, server lists %d", len(static), len(res.Tools))
	}
	for _, listed := range res.Tools {
		ti, ok := static[listed.Name]
		if !ok {
			t.Errorf("server lists %s, absent from the static table", listed.Name)
			continue
		}
		if listed.Description != ti.Description {
			t.Errorf("%s: description differs", listed.Name)
		}
		in, _ := json.Marshal(listed.InputSchema)
		if !jsonEqual(t, in, ti.InputSchema) {
			t.Errorf("%s: input schema differs", listed.Name)
		}
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

// validate checks doc against the tool's declared output schema.
func validate(t *testing.T, tool string, text string) {
	t.Helper()
	var info attestmcp.ToolInfo
	for _, ti := range attestmcp.Tools() {
		if ti.Name == tool {
			info = ti
		}
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(info.OutputSchema))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("out.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("out.json")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(inst); err != nil {
		t.Errorf("%s output does not match its output schema: %v\n%s", tool, err, text)
	}
}

func TestTools_OutputsMatchDeclaredSchemas(t *testing.T) {
	root, _ := project(t)
	cs := connect(t, root)
	calls := []struct {
		tool string
		args map[string]any
	}{
		{"trace_check", map[string]any{"path": "fixtures/incident.attest.json"}},
		{"fixture_info", map[string]any{"path": "fixtures/world.attest.json"}},
		{"draft_contract", map[string]any{"fixture_path": "fixtures/incident.attest.json"}},
		{"run_contract", map[string]any{
			"fixture_path": "fixtures/world.attest.json",
			"statements":   []string{`count("refunds", equals=1, order_id="O-1042")`, `__import__("os")`},
		}},
		{"evaluate", map[string]any{
			"fixture_path": "fixtures/incident.attest.json",
			"assertions": []map[string]any{
				{"assertion_id": "a", "type": "trace", "spec": map[string]any{"check": "forbidden_tools", "tools": []string{"delete_order"}}},
			},
		}},
		{"explain_failure", map[string]any{"report_path": "report.json"}},
	}
	for _, c := range calls {
		t.Run(c.tool, func(t *testing.T) {
			r := call(t, cs, c.tool, c.args)
			if r.IsError {
				t.Fatalf("tool error: %s", r.Text)
			}
			validate(t, c.tool, r.Text)
		})
	}
}

func statuses(t *testing.T, r callResult) []string {
	t.Helper()
	var out []string
	for _, row := range r.Doc["results"].([]any) {
		out = append(out, row.(map[string]any)["status"].(string))
	}
	return out
}

func TestRunContract_PassFailAndRejectedStatements(t *testing.T) {
	root, _ := project(t)
	cs := connect(t, root)

	r := call(t, cs, "run_contract", map[string]any{
		"fixture_path": "fixtures/incident.attest.json",
		"statements": []string{
			`trajectory(["lookup_order", "issue_refund"], mode="superset")`,
			`tool_cardinality("issue_refund", per="order_id", at_most=1)`,
			`__import__("os")`,
		},
	})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if got, want := statuses(t, r), []string{"pass", "hard_fail", "error"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses %v, want %v", got, want)
	}
	rows := r.Doc["results"].([]any)
	failing := rows[1].(map[string]any)
	if !strings.Contains(failing["explanation"].(string), "issue_refund called 2×") {
		t.Errorf("explanation %q", failing["explanation"])
	}
	if got := failing["python"]; got != `.tool_cardinality("issue_refund", per="order_id", at_most=1)` {
		t.Errorf("python line %v", got)
	}
	if got := failing["typescript"]; got != `.toolCardinality("issue_refund", { per: "order_id", atMost: 1 })` {
		t.Errorf("typescript line %v", got)
	}
	rejected := rows[2].(map[string]any)
	if !strings.Contains(rejected["explanation"].(string), "unknown method") {
		t.Errorf("rejection %q", rejected["explanation"])
	}
	sum := r.Doc["summary"].(map[string]any)
	if sum["pass"] != float64(1) || sum["hard_fail"] != float64(1) || sum["error"] != float64(1) || sum["total"] != float64(3) {
		t.Errorf("summary %v", sum)
	}
}

func TestRunContract_ArbitraryCode_NeverEvaluated(t *testing.T) {
	root, _ := project(t)
	marker := filepath.Join(root, "pwned")
	cs := connect(t, root)
	attacks := []string{
		`__import__("os").system("touch ` + marker + `")`,
		`os.system("touch ` + marker + `")`,
		`tool_cardinality("a", per=open("` + marker + `", "w"))`,
		`tool_cardinality(x)`,
		`1+1`,
		`a.b.c()`,
		`f"x"`,
		`(lambda: __import__("os").system("touch ` + marker + `"))()`,
		`forbidden_tools(["a"]); __import__("os")`,
	}
	r := call(t, cs, "run_contract", map[string]any{"fixture_path": "fixtures/incident.attest.json", "statements": attacks})
	if r.IsError {
		t.Fatal(r.Text)
	}
	for i, s := range statuses(t, r) {
		if s != "error" {
			t.Errorf("statement %d (%s): status %s, want error", i, attacks[i], s)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a statement ran code")
	}
}

func TestRunContract_EffectChecksUseFixtureWorld(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "run_contract", map[string]any{
		"fixture_path": "fixtures/world.attest.json",
		"statements": []string{
			`count("refunds", equals=1, order_id="O-1042")`,
			`unique("refunds", by="refund_id")`,
		},
	})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if got, want := statuses(t, r), []string{"hard_fail", "pass"}; !reflect.DeepEqual(got, want) {
		t.Errorf("statuses %v, want %v", got, want)
	}
	first := r.Doc["results"].([]any)[0].(map[string]any)
	if first["chain_prefix"] != ".world(FIXTURE.world)" ||
		first["python"] != `.world(FIXTURE.world).count("refunds", equals=1, order_id="O-1042")` ||
		first["typescript"] != `.world(FIXTURE.world).count("refunds", { equals: 1, where: { order_id: "O-1042" } })` {
		t.Errorf("effect lines: %v | %v | %v", first["chain_prefix"], first["python"], first["typescript"])
	}
	if _, has := first["spec"].(map[string]any)["world"]; has {
		t.Error("result spec repeats the world snapshot")
	}
}

func TestRunContract_EffectWithoutWorld_NotEvaluated(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "run_contract", map[string]any{
		"fixture_path": "fixtures/incident.attest.json",
		"statements":   []string{`count("refunds", equals=1)`},
	})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if got := statuses(t, r); !reflect.DeepEqual(got, []string{"not_evaluated"}) {
		t.Errorf("statuses %v", got)
	}
}

func TestRunContract_WorldPathFromOtherFixture(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "run_contract", map[string]any{
		"fixture_path": "fixtures/incident.attest.json",
		"world_path":   "fixtures/world.attest.json",
		"statements":   []string{`count("refunds", at_most=1)`},
	})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if got := statuses(t, r); !reflect.DeepEqual(got, []string{"hard_fail"}) {
		t.Errorf("statuses %v", got)
	}
}

func TestRunContract_TooManyStatements_Rejected(t *testing.T) {
	root, _ := project(t)
	stmts := make([]string, 51)
	for i := range stmts {
		stmts[i] = `forbidden_tools(["x"])`
	}
	r := call(t, connect(t, root), "run_contract", map[string]any{"fixture_path": "fixtures/incident.attest.json", "statements": stmts})
	if !r.IsError {
		t.Error("51 statements accepted")
	}
}

func TestDraftContract_StatementsRoundTripThroughRunContract(t *testing.T) {
	root, _ := project(t)
	cs := connect(t, root)
	d := call(t, cs, "draft_contract", map[string]any{"fixture_path": "fixtures/incident.attest.json"})
	if d.IsError {
		t.Fatal(d.Text)
	}
	if d.Doc["verified"] != true || d.Doc["expect_fail"] != true {
		t.Errorf("verified=%v expect_fail=%v", d.Doc["verified"], d.Doc["expect_fail"])
	}
	var stmts, want []string
	for _, c := range d.Doc["contracts"].([]any) {
		row := c.(map[string]any)
		if row["error"] != nil {
			t.Fatalf("contract %v has no statement: %v", row["id"], row["error"])
		}
		stmts = append(stmts, row["statement"].(string))
		want = append(want, row["status"].(string))
		if !strings.HasPrefix(row["python"].(string), ".") || !strings.HasPrefix(row["typescript"].(string), ".") {
			t.Errorf("test lines of %v: %v / %v", row["id"], row["python"], row["typescript"])
		}
	}
	if len(stmts) == 0 {
		t.Fatal("no contracts drafted")
	}
	r := call(t, cs, "run_contract", map[string]any{"fixture_path": "fixtures/incident.attest.json", "statements": stmts})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if got := statuses(t, r); !reflect.DeepEqual(got, want) {
		t.Errorf("run_contract statuses %v, draft_contract statuses %v", got, want)
	}
}

func TestDraftContract_BothSources_Rejected(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "draft_contract", map[string]any{
		"fixture_path": "fixtures/incident.attest.json", "trace_path": "fixtures/incident.attest.json",
	})
	if !r.IsError {
		t.Error("both sources accepted")
	}
}

func TestFixtureInfo_DescribesFixture(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "fixture_info", map[string]any{"path": "fixtures/world.attest.json"})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if r.Doc["name"] != "world-snapshot" {
		t.Errorf("name %v", r.Doc["name"])
	}
	w := r.Doc["world"].(map[string]any)
	if w["present"] != true || w["resources"].(map[string]any)["refunds"] != float64(2) {
		t.Errorf("world %v", w)
	}
	if r.Doc["redaction"].(map[string]any)["clean"] != true {
		t.Errorf("redaction %v", r.Doc["redaction"])
	}
}

func TestFixtureInfo_NotAFixture_Rejected(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "fixture_info", map[string]any{"path": "report.json"})
	if !r.IsError || !strings.Contains(r.Text, "not a capture fixture") {
		t.Errorf("got %+v", r)
	}
}

func TestExplainFailure_RowsCarryGuideAndNextStep(t *testing.T) {
	root, _ := project(t)
	cs := connect(t, root)
	r := call(t, cs, "explain_failure", map[string]any{"report_path": "report.json"})
	if r.IsError {
		t.Fatal(r.Text)
	}
	byAssertion := map[string]map[string]any{}
	for _, f := range r.Doc["findings"].([]any) {
		row := f.(map[string]any)
		byAssertion[row["assertion_id"].(string)] = row
	}
	if len(byAssertion) != 3 {
		t.Fatalf("%d findings, want 3 (the two not_evaluated rows and the hard fail)", len(byAssertion))
	}
	if g := byAssertion["effect_refunds"]["guide"]; g != "world-fixtures" {
		t.Errorf("effect failure guide %v", g)
	}
	if g := byAssertion["replay_inc-1042"]["guide"]; g != "replay" {
		t.Errorf("replay miss guide %v", g)
	}
	if byAssertion["effect_refunds"]["file"] != "tests/test_refund.py" || byAssertion["effect_refunds"]["line"] != float64(40) {
		t.Errorf("location %v:%v", byAssertion["effect_refunds"]["file"], byAssertion["effect_refunds"]["line"])
	}

	only := call(t, cs, "explain_failure", map[string]any{"report_path": "report.json", "test_id": "tests/test_judge.py::test_tone"})
	if rows := only.Doc["findings"].([]any); len(rows) != 1 {
		t.Errorf("test filter returned %d findings, want 1", len(rows))
	}
}

func TestExplainFailure_NotAReport_Rejected(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "explain_failure", map[string]any{"report_path": "fixtures/incident.attest.json"})
	if !r.IsError {
		t.Error("a fixture was read as a report")
	}
}

func TestExplainGuideTopics_AllExistInEmbeddedGuides(t *testing.T) {
	topics, err := assets.Topics()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tp := range topics {
		have[tp.Name] = true
	}
	for _, g := range attestmcp.GuideTopics() {
		if !have[g] {
			t.Errorf("explain_failure names guide %q, which attest guide does not have", g)
		}
	}
}

func TestEvaluate_Sources(t *testing.T) {
	root, _ := project(t)
	cs := connect(t, root)
	spec := map[string]any{"check": "tool_cardinality", "tool": "issue_refund", "per": "order_id", "max": 1}
	asserts := []map[string]any{{"assertion_id": "once", "type": "trace", "spec": spec}}

	r := call(t, cs, "evaluate", map[string]any{"fixture_path": "fixtures/incident.attest.json", "assertions": asserts})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if got := statuses(t, r); !reflect.DeepEqual(got, []string{"hard_fail"}) {
		t.Errorf("statuses %v", got)
	}

	var fx struct {
		Trace json.RawMessage `json:"trace"`
	}
	data, _ := os.ReadFile(incidentFixture)
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	inline := call(t, cs, "evaluate", map[string]any{"trace": json.RawMessage(fx.Trace), "assertions": asserts})
	if inline.IsError {
		t.Fatal(inline.Text)
	}
	if got := statuses(t, inline); !reflect.DeepEqual(got, []string{"hard_fail"}) {
		t.Errorf("inline statuses %v", got)
	}

	both := call(t, cs, "evaluate", map[string]any{"fixture_path": "fixtures/incident.attest.json", "trace": json.RawMessage(fx.Trace), "assertions": asserts})
	if !both.IsError {
		t.Error("two sources accepted")
	}
}

func TestEvaluate_Validation(t *testing.T) {
	root, _ := project(t)
	cs := connect(t, root)
	ok := map[string]any{"assertion_id": "a", "type": "trace", "spec": map[string]any{"check": "forbidden_tools", "tools": []string{"x"}}}
	cases := map[string]map[string]any{
		"plugin type":     {"fixture_path": "fixtures/incident.attest.json", "assertions": []map[string]any{{"assertion_id": "a", "type": "plugin", "spec": map[string]any{}}}},
		"duplicate id":    {"fixture_path": "fixtures/incident.attest.json", "assertions": []map[string]any{ok, ok}},
		"unknown field":   {"fixture_path": "fixtures/incident.attest.json", "assertions": []map[string]any{ok}, "extra": true},
		"no source":       {"assertions": []map[string]any{ok}},
		"spec not object": {"fixture_path": "fixtures/incident.attest.json", "assertions": []map[string]any{{"assertion_id": "a", "type": "trace", "spec": "x"}}},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if r := call(t, cs, "evaluate", args); !r.IsError {
				t.Errorf("accepted: %s", r.Text)
			}
		})
	}
}

// countingTransport fails the test on any HTTP request.
type countingTransport struct{ n atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return nil, io.ErrClosedPipe
}

func TestEvaluate_JudgeAndEmbedding_NotEvaluatedWithoutNetwork(t *testing.T) {
	counter := &countingTransport{}
	prev := http.DefaultTransport
	http.DefaultTransport = counter
	t.Cleanup(func() { http.DefaultTransport = prev })
	t.Setenv("ATTEST_JUDGE_PROVIDER", "openai")
	t.Setenv("ATTEST_JUDGE_MODEL", "gpt-4.1")
	t.Setenv("OPENAI_API_KEY", "sk-test-not-real")

	root, _ := project(t)
	r := call(t, connect(t, root), "evaluate", map[string]any{
		"fixture_path": "fixtures/world.attest.json",
		"assertions": []map[string]any{
			{"assertion_id": "judge", "type": "llm_judge", "spec": map[string]any{"criteria": "polite", "rubric": "default"}},
			{"assertion_id": "embed", "type": "embedding", "spec": map[string]any{"reference": "refund issued", "threshold": 0.8}},
		},
	})
	if r.IsError {
		t.Fatal(r.Text)
	}
	for _, row := range r.Doc["results"].([]any) {
		m := row.(map[string]any)
		if m["status"] != "not_evaluated" || m["reason"] != "provider_not_configured" {
			t.Errorf("%v: status %v reason %v", m["assertion_id"], m["status"], m["reason"])
		}
	}
	if n := counter.n.Load(); n != 0 {
		t.Errorf("%d HTTP requests made, want none", n)
	}
}

func TestPathConfinement_Rejected(t *testing.T) {
	root, outside := project(t)
	secret := "TOP-SECRET-CONTENT-7f3a"
	if err := os.WriteFile(filepath.Join(outside, "secret.json"), []byte(`{"k":"`+secret+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	copyFile(t, incidentFixture, filepath.Join(outside, "outside.attest.json"))
	mustSymlink := func(target, link string) {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	mustSymlink(filepath.Join(outside, "secret.json"), filepath.Join(root, "link-file.json"))
	mustSymlink(outside, filepath.Join(root, "link-dir"))
	mustSymlink(filepath.Join(root, "link-dir"), filepath.Join(root, "link-chain"))
	mustSymlink(filepath.Join("..", "outside", "secret.json"), filepath.Join(root, "link-relative.json"))

	abs, _ := filepath.Abs(filepath.Join(outside, "secret.json"))
	paths := map[string]string{
		"dot-dot":               "../outside/secret.json",
		"dot-dot nested":        "fixtures/../../outside/secret.json",
		"absolute outside":      abs,
		"absolute root parent":  filepath.Dir(root),
		"file symlink":          "link-file.json",
		"directory symlink":     "link-dir/secret.json",
		"symlink chain":         "link-chain/secret.json",
		"relative file symlink": "link-relative.json",
		"directory":             "fixtures",
		"nul byte":              "fixtures/incident.attest.json\x00.json",
		"empty":                 "",
		"missing":               "fixtures/missing.json",
	}
	cs := connect(t, root)
	tools := map[string]func(p string) map[string]any{
		"trace_check":     func(p string) map[string]any { return map[string]any{"path": p} },
		"fixture_info":    func(p string) map[string]any { return map[string]any{"path": p} },
		"explain_failure": func(p string) map[string]any { return map[string]any{"report_path": p} },
		"draft_contract":  func(p string) map[string]any { return map[string]any{"fixture_path": p} },
		"run_contract": func(p string) map[string]any {
			return map[string]any{"fixture_path": p, "statements": []string{`forbidden_tools(["x"])`}}
		},
		"evaluate": func(p string) map[string]any {
			return map[string]any{"trace_path": p, "assertions": []map[string]any{{"assertion_id": "a", "type": "trace", "spec": map[string]any{"check": "forbidden_tools", "tools": []string{"x"}}}}}
		},
	}
	for tool, build := range tools {
		for name, p := range paths {
			t.Run(tool+"/"+name, func(t *testing.T) {
				r := call(t, cs, tool, build(p))
				if !r.IsError {
					t.Fatalf("accepted %q: %s", p, r.Text)
				}
				if strings.Contains(r.Text, secret) {
					t.Errorf("error echoes file contents: %s", r.Text)
				}
			})
		}
	}
	// A path to the same fixture that stays inside the root still works,
	// absolute or relative, so the rejections above are about confinement.
	inside, _ := filepath.EvalSymlinks(filepath.Join(root, "fixtures", "incident.attest.json"))
	for _, p := range []string{"fixtures/incident.attest.json", "./fixtures/../fixtures/incident.attest.json", inside} {
		if r := call(t, cs, "fixture_info", map[string]any{"path": p}); r.IsError {
			t.Errorf("%q rejected: %s", p, r.Text)
		}
	}
}

func TestPathConfinement_InsideSymlinkAllowed(t *testing.T) {
	root, _ := project(t)
	if err := os.Symlink("fixtures", filepath.Join(root, "alias")); err != nil {
		t.Skip(err)
	}
	if r := call(t, connect(t, root), "fixture_info", map[string]any{"path": "alias/incident.attest.json"}); r.IsError {
		t.Errorf("symlink inside the root rejected: %s", r.Text)
	}
}

func TestPathConfinement_OversizedFile_Rejected(t *testing.T) {
	root, _ := project(t)
	f, err := os.Create(filepath.Join(root, "big.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(attestmcp.MaxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r := call(t, connect(t, root), "trace_check", map[string]any{"path": "big.json"})
	if !r.IsError || !strings.Contains(r.Text, "larger than") {
		t.Errorf("got %+v", r)
	}
}

func TestPathConfinement_FIFO_Rejected(t *testing.T) {
	root, _ := project(t)
	if err := mkfifo(filepath.Join(root, "pipe.json")); err != nil {
		t.Skip(err)
	}
	r := call(t, connect(t, root), "trace_check", map[string]any{"path": "pipe.json"})
	if !r.IsError || !strings.Contains(r.Text, "not a regular file") {
		t.Errorf("got %+v", r)
	}
}

func TestServer_WritesNothing(t *testing.T) {
	root, _ := project(t)
	before := snapshotTree(t, root)
	cs := connect(t, root)
	call(t, cs, "trace_check", map[string]any{"path": "fixtures/incident.attest.json"})
	call(t, cs, "fixture_info", map[string]any{"path": "fixtures/world.attest.json"})
	call(t, cs, "draft_contract", map[string]any{"fixture_path": "fixtures/incident.attest.json"})
	call(t, cs, "run_contract", map[string]any{"fixture_path": "fixtures/incident.attest.json", "statements": []string{`forbidden_tools(["x"])`, `bogus()`}})
	call(t, cs, "explain_failure", map[string]any{"report_path": "report.json"})
	if after := snapshotTree(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("project tree changed:\n%v\n%v", before, after)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = info.Mode().String() + info.ModTime().String() + string(rune(info.Size()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestToolArguments_UnknownField_Rejected(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "trace_check", map[string]any{"path": "fixtures/incident.attest.json", "extra": 1})
	if !r.IsError || !strings.Contains(r.Text, "unknown field") {
		t.Errorf("got %+v", r)
	}
}

func TestToolArguments_WrongType_Rejected(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "fixture_info", map[string]any{"path": 5})
	if !r.IsError {
		t.Errorf("accepted: %s", r.Text)
	}
}

func TestRunContract_LargeWorldTimesManyEffects_RejectedBeforeBuildingSpecs(t *testing.T) {
	root, _ := project(t)
	var snap map[string]any
	data, _ := os.ReadFile(worldFixture)
	var fx struct {
		World map[string]any `json:"world"`
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	snap = fx.World
	snap["state"].(map[string]any)["orders"].([]any)[0].(map[string]any)["note"] = strings.Repeat("x", 1<<20)
	out, _ := json.Marshal(snap)
	if err := os.WriteFile(filepath.Join(root, "big-world.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
	stmts := make([]string, 40)
	for i := range stmts {
		stmts[i] = `count("orders", at_least=0)`
	}
	r := call(t, connect(t, root), "run_contract", map[string]any{
		"fixture_path": "fixtures/incident.attest.json", "world_path": "big-world.json", "statements": stmts,
	})
	if !r.IsError || !strings.Contains(r.Text, "too large") {
		t.Errorf("got %+v", r)
	}
}

func TestEvaluate_SchemaFileRef_NotLoaded(t *testing.T) {
	root, outside := project(t)
	secret := filepath.Join(outside, "s.json")
	if err := os.WriteFile(secret, []byte(`{"pattern":"(sk-live-abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := call(t, connect(t, root), "evaluate", map[string]any{
		"fixture_path": "fixtures/incident.attest.json",
		"assertions": []map[string]any{{"assertion_id": "s", "type": "schema",
			"spec": map[string]any{"target": "output.message", "schema": map[string]any{"$ref": "file://" + secret}}}},
	})
	if strings.Contains(r.Text, "sk-live-abc") {
		t.Errorf("file content leaked: %s", r.Text)
	}
}

func TestEvaluate_JudgeAssertionsKeepNotConfiguredReasonAfterDeterministicFailure(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "evaluate", map[string]any{
		"fixture_path": "fixtures/incident.attest.json",
		"assertions": []map[string]any{
			{"assertion_id": "fails", "type": "trace", "spec": map[string]any{"check": "tool_cardinality", "tool": "issue_refund", "per": "order_id", "max": 1}},
			{"assertion_id": "judge", "type": "llm_judge", "spec": map[string]any{"criteria": "polite", "rubric": "default"}},
			{"assertion_id": "embed", "type": "embedding", "spec": map[string]any{"reference": "x", "threshold": 0.8}},
		},
	})
	if r.IsError {
		t.Fatal(r.Text)
	}
	rows := r.Doc["results"].([]any)
	var ids []string
	for _, row := range rows {
		m := row.(map[string]any)
		ids = append(ids, m["assertion_id"].(string))
		if m["assertion_id"] == "fails" {
			if m["status"] != "hard_fail" {
				t.Errorf("deterministic assertion: %v", m["status"])
			}
			continue
		}
		if m["status"] != "not_evaluated" || m["reason"] != "provider_not_configured" {
			t.Errorf("%v: status %v reason %v, want not_evaluated/provider_not_configured", m["assertion_id"], m["status"], m["reason"])
		}
	}
	if !reflect.DeepEqual(ids, []string{"fails", "judge", "embed"}) {
		t.Errorf("results out of request order: %v", ids)
	}
}

func TestEvaluate_OversizedRegex_Rejected(t *testing.T) {
	root, _ := project(t)
	r := call(t, connect(t, root), "evaluate", map[string]any{
		"fixture_path": "fixtures/incident.attest.json",
		"assertions": []map[string]any{{"assertion_id": "re", "type": "content",
			"spec": map[string]any{"check": "regex_match", "target": "output.message", "value": strings.Repeat("a", 5000)}}},
	})
	if !r.IsError || !strings.Contains(r.Text, "regular expression is larger") {
		t.Errorf("got %+v", r)
	}
}

// The engine's contract recording (ATTEST_CONTRACTS_DIR) belongs to the
// JSON-RPC server; the MCP server must stay read-only even when it is set.
func TestEvaluate_ContractRecordingEnv_WritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATTEST_CONTRACTS_DIR", dir)
	root, _ := project(t)
	r := call(t, connect(t, root), "evaluate", map[string]any{
		"fixture_path": "fixtures/incident.attest.json",
		"assertions": []map[string]any{{"assertion_id": "a", "type": "trace",
			"spec": map[string]any{"check": "forbidden_tools", "tools": []string{"x"}}}},
	})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the MCP server recorded specs: %v", entries)
	}
}
