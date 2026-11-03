package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/attest-ai/attest/engine/internal/capture"
	"github.com/attest-ai/attest/engine/internal/contractdsl"
	"github.com/attest-ai/attest/engine/internal/redact"
	"github.com/attest-ai/attest/engine/internal/report"
	"github.com/attest-ai/attest/engine/internal/world"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// summary counts results by status. Error counts statements the grammar
// rejected; it is zero for evaluate.
type summary struct {
	Total        int `json:"total"`
	Pass         int `json:"pass"`
	SoftFail     int `json:"soft_fail"`
	HardFail     int `json:"hard_fail"`
	NotEvaluated int `json:"not_evaluated"`
	Error        int `json:"error"`
}

func (s *summary) add(status string) {
	s.Total++
	switch status {
	case types.StatusPass:
		s.Pass++
	case types.StatusSoftFail:
		s.SoftFail++
	case types.StatusHardFail:
		s.HardFail++
	case types.StatusNotEvaluated:
		s.NotEvaluated++
	default:
		s.Error++
	}
}

// ── trace_check ──

func (e *env) traceCheck(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return nil, err
	}
	data, err := e.root.ReadFile(in.Path, MaxFileBytes)
	if err != nil {
		return nil, err
	}
	r, _, err := capture.CheckData(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", in.Path, err)
	}
	return r, nil
}

// ── evaluate ──

// evaluable are the assertion types the pipeline can answer without a
// provider: the deterministic layers, and L5/L6 as not_evaluated.
var evaluable = map[string]bool{
	types.TypeSchema: true, types.TypeConstraint: true, types.TypeTrace: true, types.TypeContent: true,
	types.TypeEmbedding: true, types.TypeLLMJudge: true, types.TypeTraceTree: true, types.TypeEffect: true,
}

type assertionInput struct {
	AssertionID string          `json:"assertion_id"`
	Type        string          `json:"type"`
	Spec        json.RawMessage `json:"spec"`
}

const maxAssertions = 200

func (e *env) evaluate(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		TracePath   string           `json:"trace_path"`
		FixturePath string           `json:"fixture_path"`
		Trace       *types.Trace     `json:"trace"`
		Assertions  []assertionInput `json:"assertions"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return nil, err
	}
	if len(in.Assertions) == 0 || len(in.Assertions) > maxAssertions {
		return nil, fmt.Errorf("assertions must hold between 1 and %d items", maxAssertions)
	}
	var t *types.Trace
	switch {
	case in.Trace != nil:
		if in.TracePath != "" || in.FixturePath != "" {
			return nil, errors.New("give exactly one of trace_path, fixture_path and trace")
		}
		t = in.Trace
	default:
		src, err := e.loadSource(in.FixturePath, in.TracePath)
		if err != nil {
			return nil, err
		}
		t = src.trace
	}
	if err := prepare(t); err != nil {
		return nil, err
	}
	batch := make([]types.Assertion, 0, len(in.Assertions))
	seen := map[string]bool{}
	var unconfigured []types.Assertion
	for i, a := range in.Assertions {
		switch {
		case a.AssertionID == "" || len(a.AssertionID) > 256:
			return nil, fmt.Errorf("assertions[%d]: assertion_id must be 1 to 256 characters", i)
		case seen[a.AssertionID]:
			return nil, fmt.Errorf("assertions[%d]: assertion_id %q is repeated", i, a.AssertionID)
		case !evaluable[a.Type]:
			return nil, fmt.Errorf("assertions[%d]: type %q is not supported", i, a.Type)
		case len(a.Spec) == 0 || a.Spec[0] != '{':
			return nil, fmt.Errorf("assertions[%d]: spec must be an object", i)
		}
		if err := checkSpec(a.Spec); err != nil {
			return nil, fmt.Errorf("assertions[%d]: %w", i, err)
		}
		seen[a.AssertionID] = true
		as := types.Assertion{AssertionID: a.AssertionID, Type: a.Type, Spec: a.Spec}
		if a.Type == types.TypeEmbedding || a.Type == types.TypeLLMJudge {
			unconfigured = append(unconfigured, as)
			continue
		}
		batch = append(batch, as)
	}
	// Judge and embedding assertions are answered here, before evaluation, so a
	// deterministic failure in the batch never relabels them short_circuited.
	byID := map[string]types.AssertionResult{}
	for _, a := range unconfigured {
		byID[a.AssertionID] = types.AssertionResult{
			AssertionID: a.AssertionID, Status: types.StatusNotEvaluated, Reason: types.ReasonProviderNotConfigured,
			Explanation: fmt.Sprintf("not run: the MCP server has no %s provider and makes no model or network calls", a.Type),
			Type:        a.Type, Layer: types.LayerForType(a.Type),
		}
	}
	if len(batch) > 0 {
		res, err := e.pipeline.EvaluateBatch(ctx, t, batch)
		if err != nil {
			return nil, fmt.Errorf("evaluation failed: %w", err)
		}
		for _, r := range res.Results {
			byID[r.AssertionID] = r
		}
	}
	results := make([]types.AssertionResult, 0, len(in.Assertions))
	var sum summary
	for _, a := range in.Assertions {
		r := byID[a.AssertionID]
		results = append(results, r)
		sum.add(r.Status)
	}
	return map[string]any{"summary": sum, "results": results}, nil
}

const (
	// maxSpecBytes caps one assertion spec an agent supplies.
	maxSpecBytes = 64 << 10
	// maxPatternBytes caps a regular expression or schema pattern.
	maxPatternBytes = 4 << 10
)

// checkSpec refuses an oversized spec or an oversized regular expression in it.
func checkSpec(spec json.RawMessage) error {
	if len(spec) > maxSpecBytes {
		return fmt.Errorf("spec is larger than %d bytes", maxSpecBytes)
	}
	var doc any
	if err := json.Unmarshal(spec, &doc); err != nil {
		return errors.New("spec is not valid JSON")
	}
	return walkPatterns(doc, 0)
}

func walkPatterns(v any, depth int) error {
	if depth > 64 {
		return errors.New("spec is nested too deeply")
	}
	switch x := v.(type) {
	case map[string]any:
		regexCheck := x["check"] == "regex_match"
		for k, e := range x {
			if str, ok := e.(string); ok && len(str) > maxPatternBytes &&
				(k == "pattern" || k == "regex" || k == "regexp" || (k == "value" && regexCheck)) {
				return fmt.Errorf("regular expression is larger than %d bytes", maxPatternBytes)
			}
			if err := walkPatterns(e, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range x {
			if err := walkPatterns(e, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// ── explain_failure ──

type finding struct {
	TestID       string `json:"test_id"`
	AssertionID  string `json:"assertion_id"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	Explanation  string `json:"explanation,omitempty"`
	FailureClass string `json:"failure_class,omitempty"`
	File         string `json:"file,omitempty"`
	Line         int    `json:"line,omitempty"`
	Guide        string `json:"guide"`
	NextStep     string `json:"next_step"`
}

func (e *env) explainFailure(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		ReportPath string `json:"report_path"`
		TestID     string `json:"test_id"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return nil, err
	}
	data, err := e.root.ReadFile(in.ReportPath, MaxFileBytes)
	if err != nil {
		return nil, err
	}
	var rep report.CIReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("%s is not a report-v2 file", in.ReportPath)
	}
	if rep.ReportVersion != 2 {
		return nil, fmt.Errorf("%s is report version %d; expected 2", in.ReportPath, rep.ReportVersion)
	}
	findings := []finding{}
	for _, r := range rep.Results {
		if r.Status == types.StatusPass || (in.TestID != "" && r.TestID != in.TestID) {
			continue
		}
		step := stepFor(r.Status, r.Reason, r.Type)
		findings = append(findings, finding{
			TestID: r.TestID, AssertionID: r.AssertionID, Status: r.Status, Reason: r.Reason,
			Explanation: r.Explanation, FailureClass: r.FailureClass, File: r.File, Line: r.Line,
			Guide: step.guide, NextStep: step.next,
		})
	}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.AssertionID < b.AssertionID
	})
	return map[string]any{"report_path": in.ReportPath, "findings": findings}, nil
}

// ── draft_contract ──

type draftedContract struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Description string          `json:"description"`
	Type        string          `json:"type"`
	Statement   string          `json:"statement,omitempty"`
	Spec        json.RawMessage `json:"spec"`
	Status      string          `json:"status"`
	Explanation string          `json:"explanation"`
	ChainPrefix string          `json:"chain_prefix,omitempty"`
	Python      string          `json:"python,omitempty"`
	TypeScript  string          `json:"typescript,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func (e *env) draftContract(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		FixturePath string `json:"fixture_path"`
		TracePath   string `json:"trace_path"`
		ExpectFail  *bool  `json:"expect_fail"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return nil, err
	}
	src, err := e.loadSource(in.FixturePath, in.TracePath)
	if err != nil {
		return nil, err
	}
	expectFail := src.fixture != nil && src.fixture.Verification.ExpectFail
	if in.ExpectFail != nil {
		expectFail = *in.ExpectFail
	}
	if err := prepare(src.trace); err != nil {
		return nil, err
	}
	contracts, err := capture.Draft(src.trace, expectFail)
	if err != nil {
		return nil, fmt.Errorf("draft: %w", err)
	}
	ver, err := capture.Verify(src.trace, contracts, expectFail, src.check)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	status := map[string]capture.ContractResult{}
	for _, r := range ver.Results {
		status[r.Contract] = r
	}
	out := make([]draftedContract, 0, len(contracts))
	for _, c := range contracts {
		d := draftedContract{
			ID: c.ID, Kind: c.Kind, Description: c.Description, Type: c.Type, Spec: c.Spec,
			Status: status[c.ID].Status, Explanation: status[c.ID].Explanation,
		}
		d.Statement, d.ChainPrefix, d.Python, d.TypeScript, d.Error = render(c)
		out = append(out, d)
	}
	return map[string]any{
		"source": src.name, "expect_fail": expectFail, "verified": ver.Verified,
		"trace_check": src.check, "contracts": out,
	}, nil
}

// render turns a drafted contract into its statement and test lines. A
// contract with no statement form carries the reason instead.
func render(c capture.Contract) (statement, prefix, python, typescript, errMsg string) {
	var spec map[string]any
	if err := json.Unmarshal(c.Spec, &spec); err != nil {
		return "", "", "", "", "spec is not an object"
	}
	call, err := contractdsl.FromSpec(c.Type, spec)
	if err != nil {
		return "", "", "", "", err.Error()
	}
	if statement, err = call.Statement(); err != nil {
		return "", "", "", "", err.Error()
	}
	if python, err = call.PythonLine(); err != nil {
		return "", "", "", "", err.Error()
	}
	if typescript, err = call.TypeScriptLine(); err != nil {
		return "", "", "", "", err.Error()
	}
	return statement, call.ChainPrefix(), python, typescript, ""
}

// ── run_contract ──

type statementResult struct {
	Statement    string         `json:"statement"`
	Status       string         `json:"status"`
	Reason       string         `json:"reason,omitempty"`
	Explanation  string         `json:"explanation"`
	FailureClass string         `json:"failure_class,omitempty"`
	Spec         map[string]any `json:"spec,omitempty"`
	ChainPrefix  string         `json:"chain_prefix,omitempty"`
	Python       string         `json:"python,omitempty"`
	TypeScript   string         `json:"typescript,omitempty"`
}

const statusError = "error"

func (e *env) runContract(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Statements  []string `json:"statements"`
		FixturePath string   `json:"fixture_path"`
		TracePath   string   `json:"trace_path"`
		WorldPath   string   `json:"world_path"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return nil, err
	}
	if len(in.Statements) == 0 || len(in.Statements) > contractdsl.MaxStatements {
		return nil, fmt.Errorf("statements must hold between 1 and %d items", contractdsl.MaxStatements)
	}
	src, err := e.loadSource(in.FixturePath, in.TracePath)
	if err != nil {
		return nil, err
	}
	if err := prepare(src.trace); err != nil {
		return nil, err
	}
	snapshot, err := e.worldSnapshot(in.WorldPath, src)
	if err != nil {
		return nil, err
	}

	results := make([]statementResult, len(in.Statements))
	calls := make([]*contractdsl.Call, len(in.Statements))
	effects := 0
	for i, stmt := range in.Statements {
		results[i] = statementResult{Statement: stmt}
		call, err := contractdsl.Parse(stmt)
		if err != nil {
			results[i].Status, results[i].Explanation = statusError, err.Error()
			continue
		}
		calls[i] = call
		if call.Method.Effect {
			effects++
		}
	}
	// Each effect spec carries the world, so check the total before building any.
	if len(snapshot)*effects > MaxFileBytes {
		return nil, errors.New("the world snapshot is too large to check against this many effect statements")
	}
	var batch []types.Assertion
	batchOf := map[string]int{}
	for i, call := range calls {
		if call == nil {
			continue
		}
		spec := call.Spec()
		results[i].Spec = spec
		results[i].ChainPrefix = call.ChainPrefix()
		results[i].Python, _ = call.PythonLine()
		results[i].TypeScript, _ = call.TypeScriptLine()
		evalSpec := spec
		if call.Method.Effect {
			evalSpec = call.Spec()
			if snapshot == nil {
				evalSpec["world"] = nil
			} else {
				evalSpec["world"] = snapshot
			}
		}
		data, err := json.Marshal(evalSpec)
		if err != nil {
			results[i].Status, results[i].Explanation = statusError, "spec could not be encoded"
			continue
		}
		id := fmt.Sprintf("statement-%d", i)
		batchOf[id] = i
		batch = append(batch, types.Assertion{AssertionID: id, Type: call.Type(), Spec: data})
	}
	if len(batch) > 0 {
		res, err := e.pipeline.EvaluateBatch(ctx, src.trace, batch)
		if err != nil {
			return nil, fmt.Errorf("evaluation failed: %w", err)
		}
		for _, r := range res.Results {
			i := batchOf[r.AssertionID]
			results[i].Status, results[i].Reason = r.Status, r.Reason
			results[i].Explanation, results[i].FailureClass = r.Explanation, r.FailureClass
		}
	}
	var sum summary
	for _, r := range results {
		sum.add(r.Status)
	}
	return map[string]any{"source": src.name, "summary": sum, "results": results}, nil
}

// ── fixture_info ──

func (e *env) fixtureInfo(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return nil, err
	}
	data, err := e.root.ReadFile(in.Path, MaxFileBytes)
	if err != nil {
		return nil, err
	}
	if !capture.IsFixture(data) {
		return nil, fmt.Errorf("%s is not a capture fixture", in.Path)
	}
	fx, err := capture.Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", in.Path, err)
	}
	steps := map[string]int{}
	for _, s := range fx.Trace.Steps {
		steps[s.Type]++
	}
	contracts := make([]map[string]string, 0, len(fx.Contracts))
	for _, c := range fx.Contracts {
		contracts = append(contracts, map[string]string{"id": c.ID, "kind": c.Kind, "description": c.Description, "type": c.Type})
	}
	worldInfo, err := worldSummary(fx.World)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", in.Path, err)
	}
	hits, err := capture.CheckRedaction(data, redact.FromProcessEnv())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", in.Path, err)
	}
	redaction := make([]map[string]string, 0, len(hits))
	for _, h := range hits {
		redaction = append(redaction, map[string]string{"kind": h.Kind, "path": h.Path})
	}
	return map[string]any{
		"path": in.Path, "name": fx.Name, "source": fx.Source,
		"trace":     map[string]any{"trace_id": fx.Trace.TraceID, "agent_id": fx.Trace.AgentID, "steps": steps},
		"exchanges": len(fx.Exchanges), "contracts": contracts, "world": worldInfo,
		"verification": fx.Verification,
		"redaction":    map[string]any{"clean": len(hits) == 0, "hits": redaction},
	}, nil
}

func worldSummary(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{"present": false}, nil
	}
	if err := world.ValidateSnapshot(raw); err != nil {
		return nil, fmt.Errorf("world is not a world-v1 snapshot: %w", err)
	}
	var snap world.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, errors.New("world snapshot could not be read")
	}
	resources := map[string]int{}
	for name, records := range snap.State {
		resources[name] = len(records)
	}
	return map[string]any{
		"present": true, "world_version": snap.WorldVersion, "definition_sha256": snap.DefinitionSHA256,
		"resources": resources, "faults_fired_count": snap.FaultsFiredCount,
	}, nil
}
