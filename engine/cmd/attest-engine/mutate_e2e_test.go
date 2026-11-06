package main_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/capture"
	"github.com/attest-framework/attest/engine/pkg/types"
)

const mutateFixture = "../../testdata/mutate/inc-1042.attest.json"

// recordBatch starts an engine with ATTEST_CONTRACTS_DIR=dir, sends one
// evaluate_batch for the fixture's trace and returns the results.
func recordBatch(t *testing.T, bin, dir string, ctx *types.BatchContext, assertions []types.Assertion) []types.AssertionResult {
	t.Helper()
	fx, err := capture.LoadFixture(mutateFixture)
	if err != nil {
		t.Fatal(err)
	}
	results, rpcErr := recordBatchTrace(t, bin, dir, ctx, fx.Trace, assertions)
	if rpcErr != nil {
		t.Fatalf("evaluate_batch: %+v", rpcErr)
	}
	return results
}

// recordBatchTrace is recordBatch for an arbitrary trace; it returns the
// engine's error instead of failing the test.
func recordBatchTrace(t *testing.T, bin, dir string, ctx *types.BatchContext, tr *types.Trace, assertions []types.Assertion) ([]types.AssertionResult, *types.RPCError) {
	t.Helper()
	cmd := exec.Command(bin, "--log-level=error")
	cmd.Env = engineEnv(t.TempDir(), "ATTEST_CONTRACTS_DIR="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	rd := bufio.NewReader(stdout)
	call := func(id int, method string, params any) (json.RawMessage, *types.RPCError) {
		req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if _, err := stdin.Write(append(req, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := rd.ReadBytes('\n')
		if err != nil {
			t.Fatalf("%s: read response: %v", method, err)
		}
		var resp struct {
			Result json.RawMessage `json:"result"`
			Error  *types.RPCError `json:"error"`
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("%s: decode %s: %v", method, line, err)
		}
		return resp.Result, resp.Error
	}
	if _, rpcErr := call(1, "initialize", types.InitializeParams{SDKName: "mutate-test", SDKVersion: "0", ProtocolVersion: 1}); rpcErr != nil {
		t.Fatalf("initialize: %+v", rpcErr)
	}
	params := types.EvaluateBatchParams{Trace: *tr, Assertions: assertions, Context: ctx}
	raw, rpcErr := call(2, "evaluate_batch", params)
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("engine exit: %v", err)
	}
	if rpcErr != nil {
		return nil, rpcErr
	}
	var result types.EvaluateBatchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result.Results, nil
}

func recordedFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestEvaluateBatch_ContractsDir_RecordsSpecsWithContext(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := filepath.Join(t.TempDir(), "contracts")
	ctx := &types.BatchContext{TestID: "tests/test_refund.py::test_one", File: "tests/test_refund.py", Line: 12}
	results := recordBatch(t, bin, dir, ctx, []types.Assertion{
		{AssertionID: "a1", Type: "schema", Spec: json.RawMessage(`{"target":"output","schema":{"type":"object"}}`)},
		{AssertionID: "a2", Type: "trace", Spec: json.RawMessage(`{"check":"required_tools","tools":["issue_refund"]}`)},
	})
	if len(results) != 2 {
		t.Fatalf("%d results", len(results))
	}

	files := recordedFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("record files = %v, want one", files)
	}
	if name := filepath.Base(files[0]); !strings.HasPrefix(name, "contracts-") {
		t.Errorf("file name %q lacks the contracts- prefix", name)
	}
	if info, err := os.Stat(files[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v (%v), want 0700", info.Mode().Perm(), err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2:\n%s", len(lines), data)
	}
	var first struct {
		TestID    string `json:"test_id"`
		File      string `json:"file"`
		Line      int    `json:"line"`
		Assertion struct {
			ID   string          `json:"assertion_id"`
			Type string          `json:"type"`
			Spec json.RawMessage `json:"spec"`
		} `json:"assertion"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.TestID != ctx.TestID || first.File != ctx.File || first.Line != ctx.Line || first.Assertion.ID != "a1" || first.Assertion.Type != "schema" {
		t.Errorf("first line = %+v", first)
	}
	if !strings.Contains(string(first.Assertion.Spec), `"type":"object"`) {
		t.Errorf("spec not recorded verbatim: %s", first.Assertion.Spec)
	}
	for _, leak := range []string{"O-1042", "refund-desk", "trace_id"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("record file contains trace content %q", leak)
		}
	}

	recordBatch(t, bin, dir, nil, []types.Assertion{{AssertionID: "b1", Type: "schema", Spec: json.RawMessage(`{"target":"output","schema":{}}`)}})
	if got := recordedFiles(t, dir); len(got) != 2 {
		t.Errorf("a second engine process wrote %d files in total, want 2", len(got))
	}
}

func TestEvaluateBatch_NoContractsDir_WritesNothing(t *testing.T) {
	bin := buildEngineBinary(t)
	home := t.TempDir()
	cmd := exec.Command(bin, "--log-level=error")
	cmd.Env = engineEnv(home)
	cmd.Dir = home
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": types.InitializeParams{SDKName: "t", SDKVersion: "0", ProtocolVersion: 1}})
	stdin.Write(append(req, '\n'))
	if _, err := bufio.NewReader(stdout).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	stdin.Close()
	_ = cmd.Wait()
	if got := recordedFiles(t, home); len(got) != 0 {
		t.Errorf("recording is off but %v exist", got)
	}
}

// act1Specs are the specs output-only tests send; act24Specs add the trace
// and effect contracts.
func act1Specs() []types.Assertion {
	return []types.Assertion{
		{AssertionID: "out-object", Type: "schema", Spec: json.RawMessage(`{"target":"output","schema":{"type":"object"}}`)},
		{AssertionID: "no-168", Type: "content", Spec: json.RawMessage(`{"target":"output.message","check":"not_contains","value":"$168.00"}`)},
		{AssertionID: "judge", Type: "llm_judge", Spec: json.RawMessage(`{"target":"output.message","threshold":0.5}`)},
	}
}

func act24Specs(t *testing.T) []types.Assertion {
	t.Helper()
	fx, err := capture.LoadFixture(mutateFixture)
	if err != nil {
		t.Fatal(err)
	}
	return []types.Assertion{
		{AssertionID: "one-refund-call", Type: "trace", Spec: json.RawMessage(`{"check":"tool_cardinality","tool":"issue_refund","per":"order_id","max":1}`)},
		{AssertionID: "one-refund", Type: "effect", Spec: json.RawMessage(`{"check":"count","resource":"refunds","where":{"order_id":"O-1042"},"equals":1,"world":` + string(fx.World) + `}`)},
	}
}

type mutateDoc struct {
	Score   *float64 `json:"score"`
	Seed    uint64   `json:"seed"`
	Mutants []struct {
		ID          string `json:"id"`
		Operator    string `json:"operator"`
		Description string `json:"description"`
		Status      string `json:"status"`
		KilledBy    []struct {
			AssertionID string `json:"assertion_id"`
			TestID      string `json:"test_id"`
		} `json:"killed_by"`
	} `json:"mutants"`
	Suggestions []struct {
		MutantID string `json:"mutant_id"`
		Contract struct {
			Type string          `json:"type"`
			Spec json.RawMessage `json:"spec"`
		} `json:"contract"`
	} `json:"survivors_suggestions"`
	Skipped    int `json:"skipped_specs"`
	Applicable int `json:"applicable_specs"`
	Operators  []struct {
		Operator          string   `json:"operator"`
		Generated         int      `json:"generated"`
		Killed            int      `json:"killed"`
		Survived          int      `json:"survived"`
		NoApplicableSpecs int      `json:"no_applicable_specs"`
		Score             *float64 `json:"score"`
	} `json:"operators"`
	Excluded struct {
		SkippedSpecs []struct {
			Reason string `json:"reason"`
			Specs  int    `json:"specs"`
		} `json:"skipped_specs"`
		MutantsNoApplicableSpecs int `json:"mutants_no_applicable_specs"`
	} `json:"excluded"`
}

func runMutateJSON(t *testing.T, bin string, wantCode int, args ...string) (mutateDoc, []byte) {
	t.Helper()
	out := cliRun(t, bin, engineEnv(t.TempDir()), t.TempDir(), wantCode, append([]string{"mutate"}, append(args, "--json")...)...)
	var doc mutateDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return doc, out
}

func (d mutateDoc) find(t *testing.T, operator, prefix string) (status string, killedBy []string) {
	t.Helper()
	for _, m := range d.Mutants {
		if m.Operator == operator && strings.HasPrefix(m.Description, prefix) {
			for _, k := range m.KilledBy {
				killedBy = append(killedBy, k.AssertionID)
			}
			return m.Status, killedBy
		}
	}
	t.Fatalf("no %s mutant %q", operator, prefix)
	return "", nil
}

func TestMutate_RecordedContracts_Act1SurvivesAndLaterContractsKill(t *testing.T) {
	bin := buildEngineBinary(t)
	fixture := cliAbs(t, mutateFixture)
	act1, full := filepath.Join(t.TempDir(), "act1"), filepath.Join(t.TempDir(), "full")
	ctx := &types.BatchContext{TestID: "py/tests/test_refund_desk.py::test_reply", File: "py/tests/test_refund_desk.py", Line: 30}
	recordBatch(t, bin, act1, ctx, act1Specs())
	recordBatch(t, bin, full, ctx, act1Specs())
	recordBatch(t, bin, full, &types.BatchContext{TestID: "py/tests/test_inc.py::test_one_refund", File: "py/tests/test_inc.py", Line: 9}, act24Specs(t))

	before, _ := runMutateJSON(t, bin, 0, fixture, "--contracts", act1)
	if status, by := before.find(t, "duplicate_tool_call", "duplicate issue_refund"); status != "survived" || len(by) != 0 {
		t.Fatalf("act 1: duplicate issue_refund is %s, killed by %v", status, by)
	}
	if status, _ := before.find(t, "alter_world_effect", "duplicate refunds record 0"); status != "survived" {
		t.Fatalf("act 1: world duplicate is %s", status)
	}
	if before.Skipped != 1 || before.Applicable != 2 {
		t.Errorf("act 1: skipped %d applicable %d, want 1 and 2 (the judge spec is skipped)", before.Skipped, before.Applicable)
	}
	foundSuggestion := false
	for _, sg := range before.Suggestions {
		if strings.Contains(sg.MutantID, ":duplicate_tool_call:") && strings.Contains(string(sg.Contract.Spec), "tool_cardinality") {
			foundSuggestion = true
		}
	}
	if !foundSuggestion {
		t.Errorf("act 1: no tool_cardinality suggestion for a duplicate_tool_call survivor: %+v", before.Suggestions)
	}

	after, _ := runMutateJSON(t, bin, 0, fixture, "--contracts", full)
	status, by := after.find(t, "duplicate_tool_call", "duplicate issue_refund")
	if status != "killed" || len(by) != 1 || by[0] != "one-refund-call" {
		t.Fatalf("full suite: duplicate issue_refund is %s, killed by %v", status, by)
	}
	if status, by := after.find(t, "alter_world_effect", "duplicate refunds record 0"); status != "killed" || len(by) != 1 || by[0] != "one-refund" {
		t.Fatalf("full suite: world duplicate is %s, killed by %v", status, by)
	}
	for _, m := range after.Mutants {
		if m.Status == "killed" && strings.HasPrefix(m.Description, "duplicate issue_refund") && m.KilledBy[0].TestID == "" {
			t.Errorf("killed_by lost the recorded test id: %+v", m)
		}
	}
	if before.Score == nil || after.Score == nil || *after.Score <= *before.Score {
		t.Errorf("score did not rise: %v -> %v", before.Score, after.Score)
	}
}

func TestMutate_FixedSeed_ByteIdenticalOutput(t *testing.T) {
	bin := buildEngineBinary(t)
	fixture := cliAbs(t, mutateFixture)
	for _, args := range [][]string{{fixture}, {fixture, "--seed", "7", "--max-mutants", "6"}} {
		_, a := runMutateJSON(t, bin, 0, args...)
		_, b := runMutateJSON(t, bin, 0, args...)
		if string(a) != string(b) {
			t.Errorf("%v: two runs differ", args)
		}
	}
	doc, _ := runMutateJSON(t, bin, 0, fixture, "--seed", "7", "--max-mutants", "6")
	if len(doc.Mutants) != 6 || doc.Seed != 7 {
		t.Errorf("%d mutants, seed %d, want 6 and 7", len(doc.Mutants), doc.Seed)
	}
}

func TestMutate_DraftedContracts_KillToolMutants(t *testing.T) {
	bin := buildEngineBinary(t)
	doc, _ := runMutateJSON(t, bin, 0, cliAbs(t, mutateFixture))
	if status, _ := doc.find(t, "duplicate_tool_call", "duplicate issue_refund"); status != "killed" {
		t.Errorf("drafted tool_cardinality did not kill duplicate issue_refund: %s", status)
	}
}

func TestMutate_ExitCodes(t *testing.T) {
	bin := buildEngineBinary(t)
	fixture := cliAbs(t, mutateFixture)
	tmp := t.TempDir()

	judgeOnly := filepath.Join(tmp, "judge")
	recordBatch(t, bin, judgeOnly, nil, []types.Assertion{{AssertionID: "j", Type: "llm_judge", Spec: json.RawMessage(`{"target":"output.message","threshold":0.5}`)}})
	doc, _ := runMutateJSON(t, bin, 1, fixture, "--contracts", judgeOnly)
	if doc.Score != nil {
		t.Errorf("score = %v, want null", *doc.Score)
	}

	emptyDir := filepath.Join(tmp, "empty")
	if err := os.MkdirAll(emptyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	notFixture := filepath.Join(tmp, "x.json")
	cliWriteFile(t, notFixture, "{}")
	for name, args := range map[string][]string{
		"no fixture":          {"mutate"},
		"missing fixture":     {"mutate", filepath.Join(tmp, "nope.attest.json")},
		"not a fixture":       {"mutate", notFixture},
		"empty contracts dir": {"mutate", fixture, "--contracts", emptyDir},
		"missing contracts":   {"mutate", fixture, "--contracts", filepath.Join(tmp, "nope")},
		"negative cap":        {"mutate", fixture, "--max-mutants", "-1"},
	} {
		if out := cliRun(t, bin, engineEnv(t.TempDir()), tmp, 2, args...); len(out) != 0 {
			t.Errorf("%s: stdout = %q, want empty on a usage or load error", name, out)
		}
	}
}

func TestMutate_HumanOutput_ShowsScoreAndSurvivors(t *testing.T) {
	bin := buildEngineBinary(t)
	act1 := filepath.Join(t.TempDir(), "act1")
	recordBatch(t, bin, act1, nil, act1Specs())
	out := string(cliRun(t, bin, engineEnv(t.TempDir()), t.TempDir(), 0, "mutate", cliAbs(t, mutateFixture), "--contracts", act1))
	for _, want := range []string{"mutation score:", "survivors:", "duplicate issue_refund (order_id=O-1042)", "kill with: trace", "tool_cardinality"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestMutate_ReportsScoresPerOperatorInJSONAndText(t *testing.T) {
	bin := buildEngineBinary(t)
	act1 := filepath.Join(t.TempDir(), "act1")
	recordBatch(t, bin, act1, nil, act1Specs())
	fixture := cliAbs(t, mutateFixture)
	doc, _ := runMutateJSON(t, bin, 0, fixture, "--contracts", act1)
	if len(doc.Operators) == 0 {
		t.Fatal("json report has no operators")
	}
	var generated int
	for _, st := range doc.Operators {
		generated += st.Generated
		if st.Generated != st.Killed+st.Survived+st.NoApplicableSpecs {
			t.Errorf("%s: %+v does not add up", st.Operator, st)
		}
	}
	if generated != len(doc.Mutants) {
		t.Errorf("operators cover %d of %d mutants", generated, len(doc.Mutants))
	}
	out := string(cliRun(t, bin, engineEnv(t.TempDir()), t.TempDir(), 0, "mutate", fixture, "--contracts", act1))
	for _, want := range []string{"operator", "generated", "score", "alter_world_effect", "excluded from the score:"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output lacks %q:\n%s", want, out)
		}
	}
	for _, st := range doc.Operators {
		if st.Score != nil && !strings.Contains(out, fmt.Sprintf("%.0f%%", *st.Score*100)) {
			t.Errorf("text output lacks the %s score %.0f%%:\n%s", st.Operator, *st.Score*100, out)
		}
	}
}

func TestMutate_DoubleDash_TreatsTheRestAsPaths(t *testing.T) {
	bin := buildEngineBinary(t)
	tmp := t.TempDir()

	// "--json" after "--" is a path, so the fixture list is not empty but its
	// last entry cannot be loaded: exit 2 with nothing on stdout.
	out := cliRun(t, bin, engineEnv(t.TempDir()), tmp, 2, "mutate", "--", cliAbs(t, mutateFixture), "--json")

	if len(out) != 0 {
		t.Errorf("stdout = %q", out)
	}
}

func TestMutate_ApplicableSpecsButNoMutants_ExitsZeroWithNullScore(t *testing.T) {
	bin := buildEngineBinary(t)
	tmp := t.TempDir()
	data, err := os.ReadFile(mutateFixture)
	if err != nil {
		t.Fatal(err)
	}
	var fx map[string]any
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	fx["world"] = nil
	trace := fx["trace"].(map[string]any)
	trace["steps"] = []any{}
	trace["turns"] = []any{}
	fx["contracts"] = []any{map[string]any{
		"id": "out-object", "kind": "output", "description": "output is an object", "type": "schema",
		"spec": map[string]any{"target": "output", "schema": map[string]any{"type": "object"}},
	}}
	fx["verification"].(map[string]any)["results"] = []any{
		map[string]any{"contract": "out-object", "status": "pass", "explanation": "output is an object"},
	}
	fixture := filepath.Join(tmp, "empty.attest.json")
	out, _ := json.Marshal(fx)
	cliWriteFile(t, fixture, string(out))

	doc, _ := runMutateJSON(t, bin, 0, fixture)

	if doc.Score != nil || len(doc.Mutants) != 0 || doc.Applicable != 1 {
		t.Errorf("score %v, %d mutants, %d applicable", doc.Score, len(doc.Mutants), doc.Applicable)
	}
}

func TestEvaluateBatch_InvalidTraceOrUnknownType_RecordsOnlyValidBatchesOfKnownTypes(t *testing.T) {
	bin := buildEngineBinary(t)
	fx, err := capture.LoadFixture(mutateFixture)
	if err != nil {
		t.Fatal(err)
	}
	spec := json.RawMessage(`{"target":"output","schema":{"type":"object"}}`)

	bad := *fx.Trace
	bad.Steps = append([]types.Step(nil), fx.Trace.Steps...)
	five := 5
	bad.Steps[0].RetryOf = &five
	rejected := filepath.Join(t.TempDir(), "rejected")
	if _, rpcErr := recordBatchTrace(t, bin, rejected, nil, &bad, []types.Assertion{{AssertionID: "x", Type: "schema", Spec: spec}}); rpcErr == nil {
		t.Fatal("the invalid trace was accepted")
	}
	for _, f := range recordedFiles(t, rejected) {
		if data, _ := os.ReadFile(f); len(strings.TrimSpace(string(data))) != 0 {
			t.Errorf("a rejected batch was recorded: %s", data)
		}
	}

	mixed := filepath.Join(t.TempDir(), "mixed")
	recordBatchTrace(t, bin, mixed, nil, fx.Trace, []types.Assertion{{AssertionID: "ok", Type: "schema", Spec: spec}, {AssertionID: "odd", Type: "no_such_type", Spec: spec}})
	files := recordedFiles(t, mixed)
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), `"ok"`) || strings.Contains(string(data), "no_such_type") {
		t.Errorf("recorded: %s", data)
	}
}
