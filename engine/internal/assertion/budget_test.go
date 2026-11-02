package assertion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/attest-ai/attest/engine/internal/assertion/judge"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/pkg/types"
)

func intPtr(n int) *int           { return &n }
func floatPtr(f float64) *float64 { return &f }
func scoreReply() *llm.CompletionResponse {
	return &llm.CompletionResponse{Content: `{"score": 0.9, "explanation": "ok"}`, InputTokens: 100, OutputTokens: 10}
}

func budgetJudgeAssertion(id, spec string) *types.Assertion {
	return &types.Assertion{AssertionID: id, Type: types.TypeLLMJudge, Spec: json.RawMessage(spec)}
}

func budgetJudgeTrace() *types.Trace {
	return &types.Trace{Output: json.RawMessage(`"the refund was issued once"`)}
}

func TestLedger_ConcurrentReservations_NeverExceedCallLimit(t *testing.T) {
	// Arrange
	ledger := NewLedger(BudgetLimits{MaxJudgeCalls: intPtr(10)})
	mock := llm.NewMockProvider([]*llm.CompletionResponse{scoreReply()}, nil)
	mock.SimulatedLatency = 5 * time.Millisecond
	p := NewBudgetedProvider(mock, ledger, &llm.Price{})

	// Act
	var wg sync.WaitGroup
	var mu sync.Mutex
	refused := 0
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Complete(context.Background(), &llm.CompletionRequest{MaxTokens: 256}); err != nil {
				if !errors.Is(err, llm.ErrBudgetExhausted) {
					t.Errorf("unexpected error: %v", err)
				}
				mu.Lock()
				refused++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Assert
	if mock.CallCount != 10 || refused != 40 {
		t.Fatalf("inner calls = %d, refused = %d; want 10 and 40", mock.CallCount, refused)
	}
	if u := ledger.Usage(); u.ModelCalls != 10 || u.BudgetRefused != 40 {
		t.Errorf("usage = %+v, want 10 calls and 40 refused", u)
	}
}

func TestLedger_USDReservation_HoldsInFlightAndReconcilesToUsage(t *testing.T) {
	// Arrange: $1/1M in and out. A 256-token output limit plus a small
	// prompt reserves about $0.000280; actual usage costs $0.000110.
	ledger := NewLedger(BudgetLimits{MaxCostUSD: floatPtr(0.0003)})
	price := llm.Price{InPer1M: 1, OutPer1M: 1}
	req := &llm.CompletionRequest{MaxTokens: 256, Messages: []llm.Message{{Role: "user", Content: "x"}}}
	scope := ledger.NewScope(nil)

	first, err := ledger.reserve(scope, true, price.Cost(inputTokenBound(req), 256), true, "m")
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}

	// Act + Assert: a second call cannot reserve while the first is in flight.
	if _, err := ledger.reserve(scope, true, price.Cost(inputTokenBound(req), 256), true, "m"); !errors.Is(err, llm.ErrBudgetExhausted) {
		t.Fatalf("second reserve during flight: err = %v, want budget exhausted", err)
	}
	first.settle(types.Usage{ModelCalls: 1, InputTokens: 100, OutputTokens: 10, CostUSD: price.Cost(100, 10)})
	if _, err := ledger.reserve(scope, true, price.Cost(100, 10), true, "m"); err != nil {
		t.Fatalf("reserve after settle: %v", err)
	}
	if got := scope.Usage().CostUSD; got != price.Cost(100, 10) {
		t.Errorf("scope cost = %v, want reconciled %v", got, price.Cost(100, 10))
	}
}

func TestLedger_ScopeLimit_RefusesWithinSessionHeadroom(t *testing.T) {
	ledger := NewLedger(BudgetLimits{})
	scope := ledger.NewScope(&types.BatchBudget{MaxJudgeCalls: intPtr(1)})

	if _, err := ledger.reserve(scope, true, 0, true, "m"); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	_, err := ledger.reserve(scope, true, 0, true, "m")

	if !errors.Is(err, llm.ErrBudgetExhausted) || !strings.Contains(err.Error(), "test budget") {
		t.Fatalf("err = %v, want a test-budget refusal", err)
	}
	if _, err := ledger.reserve(nil, true, 0, true, "m"); err != nil {
		t.Errorf("session reserve outside the scope: %v", err)
	}
}

func TestLedger_USDLimitWithUnpricedModel_Refuses(t *testing.T) {
	ledger := NewLedger(BudgetLimits{MaxCostUSD: floatPtr(1)})

	_, err := ledger.reserve(nil, true, 0, false, "mystery-model")

	if !errors.Is(err, llm.ErrBudgetExhausted) || !strings.Contains(err.Error(), "no price") {
		t.Fatalf("err = %v, want a no-price refusal", err)
	}
}

func TestBudgetedProvider_RetryAttempt_CountsRetryCost(t *testing.T) {
	ledger := NewLedger(BudgetLimits{})
	p := NewBudgetedProvider(llm.NewMockProvider([]*llm.CompletionResponse{scoreReply()}, nil), ledger, &llm.Price{InPer1M: 1, OutPer1M: 1})

	if _, err := p.Complete(llm.WithAttempt(context.Background(), 1), &llm.CompletionRequest{}); err != nil {
		t.Fatal(err)
	}

	u := ledger.Usage()
	if u.RetryCostUSD == 0 || u.RetryCostUSD != u.CostUSD {
		t.Errorf("usage = %+v, want the whole cost attributed to the retry", u)
	}
}

func TestBudgetedProvider_UnpricedModelWithoutUSDLimit_CountsUnpriced(t *testing.T) {
	ledger := NewLedger(BudgetLimits{})
	p := NewBudgetedProvider(llm.NewMockProvider([]*llm.CompletionResponse{scoreReply()}, nil), ledger, nil)

	if _, err := p.Complete(context.Background(), &llm.CompletionRequest{Model: "mystery-model"}); err != nil {
		t.Fatal(err)
	}

	if u := ledger.Usage(); u.ModelCalls != 1 || u.UnpricedCalls != 1 || u.CostUSD != 0 {
		t.Errorf("usage = %+v, want one unpriced call", u)
	}
}

func TestJudgeEvaluator_BudgetRefusesCall_NotEvaluatedBudgetExhausted(t *testing.T) {
	ledger := NewLedger(BudgetLimits{MaxJudgeCalls: intPtr(0)})
	mock := llm.NewMockProvider([]*llm.CompletionResponse{scoreReply()}, nil)
	e := NewJudgeEvaluator(NewBudgetedProvider(mock, ledger, &llm.Price{}), judge.NewRubricRegistry(), nil)

	r := e.Evaluate(budgetJudgeTrace(), budgetJudgeAssertion("j", `{"target":"output"}`))

	if r.Status != types.StatusNotEvaluated || r.Reason != types.ReasonBudgetExhausted || mock.CallCount != 0 {
		t.Fatalf("status=%s reason=%s calls=%d, want not_evaluated(budget_exhausted) and no call", r.Status, r.Reason, mock.CallCount)
	}
}

func TestJudgeEvaluator_RepeatPartlyRefused_ScoresSentRunsAndNotesRefusal(t *testing.T) {
	ledger := NewLedger(BudgetLimits{MaxJudgeCalls: intPtr(2)})
	mock := llm.NewMockProvider([]*llm.CompletionResponse{scoreReply()}, nil)
	e := NewJudgeEvaluator(NewBudgetedProvider(mock, ledger, &llm.Price{}), judge.NewRubricRegistry(), nil)

	r := e.Evaluate(budgetJudgeTrace(), budgetJudgeAssertion("j", `{"target":"output","repeat":5,"threshold":0.5}`))

	if r.Status != types.StatusPass || len(r.Judge.SampleScores) != 2 {
		t.Fatalf("status=%s samples=%v, want pass over the 2 sent runs", r.Status, r.Judge.SampleScores)
	}
	if !strings.Contains(r.Explanation, "3 of 5 runs not scored: 3 not sent: budget exhausted") {
		t.Errorf("explanation %q does not record the refused runs", r.Explanation)
	}
}

func TestJudgeEvaluator_AllRepeatsAbstain_NotEvaluatedJudgeAbstained(t *testing.T) {
	mock := llm.NewMockProvider([]*llm.CompletionResponse{{Content: `{"abstain": true, "reason": "cannot determine"}`}}, nil)
	e := NewJudgeEvaluator(mock, judge.NewRubricRegistry(), nil)

	r := e.Evaluate(budgetJudgeTrace(), budgetJudgeAssertion("j", `{"target":"output","repeat":3}`))

	if r.Status != types.StatusNotEvaluated || r.Reason != types.ReasonJudgeAbstained {
		t.Fatalf("status=%s reason=%s, want not_evaluated(judge_abstained)", r.Status, r.Reason)
	}
	if !strings.Contains(r.Explanation, "cannot determine") {
		t.Errorf("explanation %q lacks the abstain reason", r.Explanation)
	}
}

func TestJudgeEvaluator_Abstain_CarriesJudgeMetadata(t *testing.T) {
	mock := llm.NewMockProvider([]*llm.CompletionResponse{{Content: `{"abstain": true, "reason": "no order id"}`}}, nil)
	e := NewJudgeEvaluator(mock, judge.NewRubricRegistry(), nil)
	a := budgetJudgeAssertion("j", `{"target":"output"}`)

	first := e.Evaluate(budgetJudgeTrace(), a)

	if first.Reason != types.ReasonJudgeAbstained || first.Judge == nil || first.Judge.Model != "mock-model" {
		t.Fatalf("result = %+v, want judge_abstained with judge metadata", first)
	}
}

func TestBiasProbes_AbstainingProbe_IsSkipped(t *testing.T) {
	mock := llm.NewMockProvider([]*llm.CompletionResponse{{Content: `{"abstain": true, "reason": "x"}`}}, nil)
	rubric, _ := judge.NewRubricRegistry().Get("default")

	got, _ := runBiasProbes(context.Background(), mock, rubric, "m", "content", 0.5, []string{"position"})

	if len(got) != 0 {
		t.Errorf("probes = %+v, want the abstaining probe skipped", got)
	}
}

func TestPipeline_BudgetScopeInContext_ReachesJudge(t *testing.T) {
	// Arrange: two judge assertions, a batch scope allowing one call.
	ledger := NewLedger(BudgetLimits{})
	mock := llm.NewMockProvider([]*llm.CompletionResponse{scoreReply()}, nil)
	reg := NewRegistry(WithJudge(NewBudgetedProvider(mock, ledger, &llm.Price{}), judge.NewRubricRegistry(), nil))
	p := NewPipeline(reg)
	trace := &types.Trace{Output: json.RawMessage(`{"a":"one","b":"two"}`)}
	var as []types.Assertion
	for _, k := range []string{"a", "b"} {
		as = append(as, *budgetJudgeAssertion("j-"+k, fmt.Sprintf(`{"target":"output.%s"}`, k)))
	}
	scope := ledger.NewScope(&types.BatchBudget{MaxJudgeCalls: intPtr(1)})

	// Act
	res, err := p.EvaluateBatch(WithBudgetScope(context.Background(), scope), trace, as)

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	refused := 0
	for _, r := range res.Results {
		if r.Reason == types.ReasonBudgetExhausted {
			refused++
		}
	}
	if refused != 1 || mock.CallCount != 1 || scope.Usage().ModelCalls != 1 {
		t.Errorf("refused=%d calls=%d scope usage=%+v, want 1/1/1", refused, mock.CallCount, scope.Usage())
	}
}
