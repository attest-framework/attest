package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/attest-framework/attest/engine/internal/policy"
	"github.com/attest-framework/attest/engine/pkg/types"
)

// scriptedJudge is an OpenAI-compatible judge endpoint that counts the
// requests it receives, like the demo's model server.
type scriptedJudge struct {
	srv      *httptest.Server
	requests atomic.Int32
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

// newScriptedJudge serves reply as the judge message content to every
// request, holding each request for delay so concurrent calls overlap.
func newScriptedJudge(t *testing.T, reply string, delay time.Duration) *scriptedJudge {
	t.Helper()
	j := &scriptedJudge{}
	content, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	j.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.requests.Add(1)
		n := j.inFlight.Add(1)
		defer j.inFlight.Add(-1)
		for {
			seen := j.maxSeen.Load()
			if n <= seen || j.maxSeen.CompareAndSwap(seen, n) {
				break
			}
		}
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"model":"scripted-judge","choices":[{"message":{"role":"assistant","content":%s}}],`+
			`"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":40}}}`, content)
	}))
	t.Cleanup(j.srv.Close)
	return j
}

// useScriptedJudge points the engine's judge at j with a rate limit high
// enough not to throttle the test.
func useScriptedJudge(t *testing.T, j *scriptedJudge) {
	t.Helper()
	withoutProviders(t, "")
	t.Setenv("ATTEST_JUDGE_BASE_URL", j.srv.URL+"/v1")
	t.Setenv("ATTEST_JUDGE_API_KEY", "judge-key")
	t.Setenv("ATTEST_JUDGE_MODEL", "scripted-judge")
	t.Setenv("ATTEST_RATE_LIMIT_OPENAI", "60000")
	t.Setenv("ATTEST_RATE_LIMIT_OPENAI_BURST", "1000")
	t.Setenv("ATTEST_EVAL_CONCURRENCY", "64")
	t.Setenv("ATTEST_MAX_JUDGE_CALLS", "")
	t.Setenv("ATTEST_BUDGET_USD", "")
	t.Setenv("ATTEST_JUDGE_PRICE_IN", "")
	t.Setenv("ATTEST_JUDGE_PRICE_OUT", "")
}

// judgeBatchOf builds n judge assertions over distinct outputs, so the
// judge cache never answers one of them.
func judgeBatchOf(n int) types.EvaluateBatchParams {
	out := make(map[string]string, n)
	assertions := make([]types.Assertion, n)
	for i := range n {
		key := fmt.Sprintf("m%d", i)
		out[key] = fmt.Sprintf("refund answer %d", i)
		assertions[i] = types.Assertion{
			AssertionID: fmt.Sprintf("judge-%02d", i),
			Type:        types.TypeLLMJudge,
			Spec:        json.RawMessage(fmt.Sprintf(`{"target":"output.%s","criteria":"helpful"}`, key)),
		}
	}
	output, _ := json.Marshal(out)
	return types.EvaluateBatchParams{
		Trace: types.Trace{
			SchemaVersion: 1,
			TraceID:       "trace-budget",
			AgentID:       "agent-1",
			Input:         json.RawMessage(`"refund order 1042"`),
			Output:        output,
		},
		Assertions: assertions,
	}
}

// runBatch initializes an engine session and evaluates batch.
func runBatch(t *testing.T, batch types.EvaluateBatchParams) types.EvaluateBatchResult {
	t.Helper()
	stdin, stdout, _ := newTestServer(t)
	sendRequest(t, stdin, 1, "initialize", initializeParams())
	if resp := readResponse(t, stdout); resp.Error != nil {
		t.Fatalf("initialize: %+v", resp.Error)
	}
	sendRequest(t, stdin, 2, "evaluate_batch", batch)
	resp := readResponse(t, stdout)
	if resp.Error != nil {
		t.Fatalf("evaluate_batch: %+v", resp.Error)
	}
	var res types.EvaluateBatchResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decode evaluate_batch: %v", err)
	}
	return res
}

func countByReason(results []types.AssertionResult) (passed int, reasons map[string]int) {
	reasons = make(map[string]int)
	for _, r := range results {
		switch r.Status {
		case types.StatusPass:
			passed++
		case types.StatusNotEvaluated:
			reasons[r.Reason]++
		}
	}
	return passed, reasons
}

func TestHandler_FiftyConcurrentJudges_CallBudgetOfTen_SendsAtMostTen(t *testing.T) {
	// Arrange
	judge := newScriptedJudge(t, `{"score": 0.9, "explanation": "helpful"}`, 50*time.Millisecond)
	useScriptedJudge(t, judge)
	t.Setenv("ATTEST_MAX_JUDGE_CALLS", "10")

	// Act
	res := runBatch(t, judgeBatchOf(50))

	// Assert
	t.Logf("model server requests=%d, max in flight=%d", judge.requests.Load(), judge.maxSeen.Load())
	if got := judge.requests.Load(); got != 10 {
		t.Fatalf("model server counted %d requests, want exactly the 10 the budget covers", got)
	}
	if judge.maxSeen.Load() < 2 {
		t.Errorf("max in-flight judge requests = %d; the judges did not run concurrently", judge.maxSeen.Load())
	}
	passed, reasons := countByReason(res.Results)
	if passed != int(judge.requests.Load()) || reasons[types.ReasonBudgetExhausted] != 50-passed {
		t.Fatalf("passed=%d reasons=%v, want every unsent judge not_evaluated(budget_exhausted)", passed, reasons)
	}
	if res.Usage.ModelCalls != passed || res.Usage.JudgeCalls != passed || res.Usage.BudgetRefused != 50-passed {
		t.Errorf("usage = %+v, want %d calls and %d refused", res.Usage, passed, 50-passed)
	}
	if exit := policy.Evaluate(&policy.Policy{}, res.Results, res.TotalCost, nil); exit.ExitCode() != policy.ExitWarn {
		t.Errorf("policy exit = %d (%+v), want %d", exit.ExitCode(), exit.Violations, policy.ExitWarn)
	}
}

func TestHandler_JudgeAbstains_ReturnsNotEvaluatedJudgeAbstained(t *testing.T) {
	// Arrange
	judge := newScriptedJudge(t, `{"abstain": true, "reason": "cannot determine"}`, 0)
	useScriptedJudge(t, judge)

	// Act
	res := runBatch(t, judgeBatchOf(1))

	// Assert
	r := res.Results[0]
	if r.Status != types.StatusNotEvaluated || r.Reason != types.ReasonJudgeAbstained {
		t.Fatalf("status=%q reason=%q, want not_evaluated(judge_abstained)", r.Status, r.Reason)
	}
	if !strings.Contains(r.Explanation, "cannot determine") {
		t.Errorf("explanation %q does not carry the judge's reason", r.Explanation)
	}
}

func TestHandler_BatchBudget_LimitsCallsWithinOneBatch(t *testing.T) {
	// Arrange
	judge := newScriptedJudge(t, `{"score": 0.9, "explanation": "helpful"}`, 0)
	useScriptedJudge(t, judge)
	batch := judgeBatchOf(5)
	two := 2
	batch.Budget = &types.BatchBudget{MaxJudgeCalls: &two}

	// Act
	res := runBatch(t, batch)

	// Assert
	if got := judge.requests.Load(); got != 2 {
		t.Fatalf("model server counted %d requests, want 2", got)
	}
	if _, reasons := countByReason(res.Results); reasons[types.ReasonBudgetExhausted] != 3 {
		t.Errorf("reasons = %v, want 3 budget_exhausted", reasons)
	}
}

func TestHandler_USDBudgetWithPriceOverride_ReconcilesCostToUsage(t *testing.T) {
	// Arrange: $1 per 1M tokens in and out; 120 tokens reported per call.
	judge := newScriptedJudge(t, `{"score": 0.9, "explanation": "helpful"}`, 0)
	useScriptedJudge(t, judge)
	t.Setenv("ATTEST_BUDGET_USD", "1")
	t.Setenv("ATTEST_JUDGE_PRICE_IN", "1")
	t.Setenv("ATTEST_JUDGE_PRICE_OUT", "1")

	// Act
	res := runBatch(t, judgeBatchOf(3))

	// Assert
	want := 3 * 120.0 / 1_000_000
	if diff := res.Usage.CostUSD - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("usage cost = %v, want %v", res.Usage.CostUSD, want)
	}
	if res.Usage.InputTokens != 300 || res.Usage.OutputTokens != 60 || res.Usage.CachedTokens != 120 {
		t.Errorf("usage tokens = %+v, want 300 in, 60 out, 120 cached", res.Usage)
	}
	if res.Results[0].Cost == 0 {
		t.Error("judge result cost is 0; want the reconciled per-call cost")
	}
}

func TestBuildProviders_USDBudgetWithUnpricedJudgeModel_FailsStartup(t *testing.T) {
	// Arrange
	judge := newScriptedJudge(t, `{"score": 0.9, "explanation": "x"}`, 0)
	useScriptedJudge(t, judge)
	t.Setenv("ATTEST_BUDGET_USD", "0.5")

	// Act
	_, err := buildProviders(slog.New(slog.DiscardHandler))

	// Assert
	if err == nil || !strings.Contains(err.Error(), "ATTEST_JUDGE_PRICE_IN") {
		t.Fatalf("err = %v, want a startup error naming ATTEST_JUDGE_PRICE_IN", err)
	}
}

func TestResolveBudgetConfig_InvalidValues_ReturnError(t *testing.T) {
	cases := map[string]map[string]string{
		"negative calls":   {"ATTEST_MAX_JUDGE_CALLS": "-1"},
		"non-integer":      {"ATTEST_MAX_JUDGE_CALLS": "ten"},
		"negative usd":     {"ATTEST_BUDGET_USD": "-0.1"},
		"infinite usd":     {"ATTEST_BUDGET_USD": "+Inf"},
		"price in only":    {"ATTEST_JUDGE_PRICE_IN": "2"},
		"negative price":   {"ATTEST_JUDGE_PRICE_IN": "-2", "ATTEST_JUDGE_PRICE_OUT": "8"},
		"non-number price": {"ATTEST_JUDGE_PRICE_IN": "2", "ATTEST_JUDGE_PRICE_OUT": "eight"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := resolveBudgetConfig(); err == nil {
				t.Errorf("resolveBudgetConfig(%v) = nil error", env)
			}
		})
	}
}
