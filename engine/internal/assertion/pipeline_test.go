package assertion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/pkg/types"

	_ "modernc.org/sqlite"
)

func TestPipeline_EvaluateBatch_MixedTypes(t *testing.T) {
	pipeline := NewPipeline(NewRegistry())

	trace := &types.Trace{
		TraceID: "trc_pipeline_test",
		Output:  json.RawMessage(`{"message":"Hello World","structured":{"score":0.9}}`),
		Steps: []types.Step{
			{
				Name:   "search",
				Type:   types.StepTypeToolCall,
				Args:   json.RawMessage(`{"query":"test"}`),
				Result: json.RawMessage(`{"hits":3}`),
			},
		},
	}

	assertions := []types.Assertion{
		{
			AssertionID: "content_assert",
			Type:        types.TypeContent,
			Spec:        json.RawMessage(`{"target":"output.message","check":"contains","value":"Hello"}`),
		},
		{
			AssertionID: "schema_assert",
			Type:        types.TypeSchema,
			Spec: json.RawMessage(`{
				"target": "output.structured",
				"schema": {
					"type": "object",
					"required": ["score"],
					"properties": {"score": {"type": "number"}}
				}
			}`),
		},
		{
			AssertionID: "trace_assert",
			Type:        types.TypeTrace,
			Spec:        json.RawMessage(`{"check":"required_tools","tools":["search"]}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), trace, assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch returned error: %v", err)
	}
	if len(result.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result.Results))
	}

	// All should pass.
	for _, r := range result.Results {
		if r.Status != types.StatusPass {
			t.Errorf("assertion %q: got status %q, want pass; explanation: %s", r.AssertionID, r.Status, r.Explanation)
		}
	}
}

func TestPipeline_EvaluateBatch_UnknownType(t *testing.T) {
	pipeline := NewPipeline(NewRegistry())

	trace := &types.Trace{
		TraceID: "trc_unknown_type",
		Output:  json.RawMessage(`{"message":"ok"}`),
	}

	assertions := []types.Assertion{
		{
			AssertionID: "good_assert",
			Type:        types.TypeContent,
			Spec:        json.RawMessage(`{"target":"output.message","check":"contains","value":"ok"}`),
		},
		{
			AssertionID: "bad_assert",
			Type:        "llm_judge", // not registered in built-in registry
			Spec:        json.RawMessage(`{}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), trace, assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch returned error: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}

	// Find the bad assert result.
	var badResult *types.AssertionResult
	for i := range result.Results {
		if result.Results[i].AssertionID == "bad_assert" {
			badResult = &result.Results[i]
		}
	}
	if badResult == nil {
		t.Fatal("bad_assert result not found")
	}
	if badResult.Status != types.StatusHardFail {
		t.Errorf("bad_assert: got status %q, want hard_fail", badResult.Status)
	}
}

func TestPipeline_EvaluateBatch_LayerOrder(t *testing.T) {
	pipeline := NewPipeline(NewRegistry())

	trace := &types.Trace{
		TraceID: "trc_order_test",
		Output:  json.RawMessage(`{"message":"test","structured":{"key":"val"}}`),
		Steps: []types.Step{
			{
				Name:   "tool_a",
				Type:   types.StepTypeToolCall,
				Result: json.RawMessage(`{}`),
			},
		},
	}

	// Submit in reverse order; expect results to be in evaluation order.
	assertions := []types.Assertion{
		{
			AssertionID: "content_4",
			Type:        types.TypeContent,
			Spec:        json.RawMessage(`{"target":"output.message","check":"contains","value":"test"}`),
		},
		{
			AssertionID: "trace_3",
			Type:        types.TypeTrace,
			Spec:        json.RawMessage(`{"check":"required_tools","tools":["tool_a"]}`),
		},
		{
			AssertionID: "schema_1",
			Type:        types.TypeSchema,
			Spec: json.RawMessage(`{
				"target": "output.structured",
				"schema": {"type": "object"}
			}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), trace, assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch returned error: %v", err)
	}
	if len(result.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result.Results))
	}

	// Results should be in layer order: schema(1), trace(3), content(4).
	wantOrder := []string{"schema_1", "trace_3", "content_4"}
	for i, want := range wantOrder {
		if result.Results[i].AssertionID != want {
			t.Errorf("result[%d].AssertionID = %q, want %q", i, result.Results[i].AssertionID, want)
		}
	}
}

// trackingEvaluator counts concurrent invocations and records the maximum
// observed concurrency. Used to verify the L5/L6 worker pool cap.
type trackingEvaluator struct {
	maxConcurrent atomic.Int32
	live          atomic.Int32
	delay         time.Duration
	blockUntil    chan struct{}
}

func (e *trackingEvaluator) Evaluate(_ *types.Trace, a *types.Assertion) *types.AssertionResult {
	live := e.live.Add(1)
	for {
		cur := e.maxConcurrent.Load()
		if live <= cur {
			break
		}
		if e.maxConcurrent.CompareAndSwap(cur, live) {
			break
		}
	}
	defer e.live.Add(-1)

	if e.blockUntil != nil {
		<-e.blockUntil
	}
	if e.delay > 0 {
		time.Sleep(e.delay)
	}

	return &types.AssertionResult{
		AssertionID: a.AssertionID,
		Status:      types.StatusPass,
		Score:       1.0,
	}
}

func makeEmbeddingAssertions(n int) []types.Assertion {
	out := make([]types.Assertion, n)
	for i := 0; i < n; i++ {
		out[i] = types.Assertion{
			AssertionID: fmt.Sprintf("emb_%d", i),
			Type:        types.TypeEmbedding,
			Spec:        json.RawMessage(`{}`),
		}
	}
	return out
}

func TestPipeline_EvaluateBatch_ConcurrencyBounded(t *testing.T) {
	evaluator := &trackingEvaluator{delay: 5 * time.Millisecond}
	registry := NewRegistry()
	registry.Register(types.TypeEmbedding, evaluator)

	pipeline := NewPipeline(registry)
	pipeline.SetEvalConcurrency(4)

	const total = 200
	assertions := makeEmbeddingAssertions(total)

	trace := &types.Trace{TraceID: "trc_concurrency", Output: json.RawMessage(`{}`)}

	result, err := pipeline.EvaluateBatch(context.Background(), trace, assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	if len(result.Results) != total {
		t.Fatalf("expected %d results, got %d", total, len(result.Results))
	}

	maxObserved := evaluator.maxConcurrent.Load()
	if maxObserved > 4 {
		t.Errorf("L5/L6 concurrency exceeded cap: observed %d > 4", maxObserved)
	}
	if maxObserved < 2 {
		t.Errorf("expected at least some parallelism, observed max=%d", maxObserved)
	}
}

func TestPipeline_EvaluateBatch_ContextCancellation(t *testing.T) {
	block := make(chan struct{})

	evaluator := &trackingEvaluator{blockUntil: block}
	registry := NewRegistry()
	registry.Register(types.TypeEmbedding, evaluator)

	pipeline := NewPipeline(registry)
	pipeline.SetEvalConcurrency(2)

	const total = 20
	assertions := makeEmbeddingAssertions(total)
	trace := &types.Trace{TraceID: "trc_cancel", Output: json.RawMessage(`{}`)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		result *BatchResult
		err    error
	}, 1)

	go func() {
		r, e := pipeline.EvaluateBatch(ctx, trace, assertions)
		done <- struct {
			result *BatchResult
			err    error
		}{r, e}
	}()

	// Wait for the first batch of workers to be in flight.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Release the in-flight workers so they can drain the wg.
	close(block)

	select {
	case res := <-done:
		if !errors.Is(res.err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got err=%v", res.err)
		}
		// The dispatch loop must have stopped after cancellation, so far
		// fewer than total assertions can have been started.
		started := evaluator.maxConcurrent.Load()
		if started > 4 {
			t.Errorf("expected dispatch to stop after cancellation, but %d workers ran", started)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EvaluateBatch did not return after cancellation")
	}
}

func TestPipeline_ApplyDynamicThreshold_StaticSpec(t *testing.T) {
	pipeline := NewPipeline(NewRegistry())
	trace := &types.Trace{TraceID: "trc_static", Output: json.RawMessage(`{"message":"ok"}`)}
	assertions := []types.Assertion{
		{
			AssertionID: "static_assert",
			Type:        types.TypeContent,
			Spec:        json.RawMessage(`{"target":"output.message","check":"contains","value":"ok"}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), trace, assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	if got := result.Results[0].ThresholdSource; got != types.ThresholdSourceStatic {
		t.Errorf("ThresholdSource = %q, want %q", got, types.ThresholdSourceStatic)
	}
}

func TestPipeline_ApplyDynamicThreshold_HistoryUnavailable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	store, err := cache.NewHistoryStore(db)
	if err != nil {
		t.Fatalf("NewHistoryStore: %v", err)
	}

	// Close the underlying db so QueryWindow returns an error.
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	pipeline := NewPipelineWithHistory(NewRegistry(), store)
	trace := &types.Trace{TraceID: "trc_dyn_unavail", Output: json.RawMessage(`{"message":"ok"}`)}
	assertions := []types.Assertion{
		{
			AssertionID: "dyn_assert",
			Type:        types.TypeContent,
			Spec: json.RawMessage(`{
				"target":"output.message",
				"check":"contains",
				"value":"ok",
				"threshold":"dynamic"
			}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), trace, assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	r := result.Results[0]
	if r.ThresholdSource != types.ThresholdSourceDynamicUnavailable {
		t.Errorf("ThresholdSource = %q, want %q", r.ThresholdSource, types.ThresholdSourceDynamicUnavailable)
	}
	if !strings.HasPrefix(r.Explanation, "[dynamic_unavailable:") {
		t.Errorf("Explanation does not carry dynamic_unavailable prefix: %q", r.Explanation)
	}
}

func TestPipeline_ApplyDynamicThreshold_NoHistoryStore(t *testing.T) {
	pipeline := NewPipeline(NewRegistry())
	trace := &types.Trace{TraceID: "trc_no_store", Output: json.RawMessage(`{"message":"ok"}`)}
	assertions := []types.Assertion{
		{
			AssertionID: "dyn_assert",
			Type:        types.TypeContent,
			Spec: json.RawMessage(`{
				"target":"output.message",
				"check":"contains",
				"value":"ok",
				"threshold":"dynamic"
			}`),
		},
	}

	result, err := pipeline.EvaluateBatch(context.Background(), trace, assertions)
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	r := result.Results[0]
	if r.ThresholdSource != types.ThresholdSourceDynamicUnavailable {
		t.Errorf("ThresholdSource = %q, want %q", r.ThresholdSource, types.ThresholdSourceDynamicUnavailable)
	}
	if !strings.Contains(r.Explanation, "history store not configured") {
		t.Errorf("Explanation missing history-store hint: %q", r.Explanation)
	}
}

func TestPipeline_EvaluateBatch_Empty(t *testing.T) {
	pipeline := NewPipeline(NewRegistry())

	trace := &types.Trace{
		TraceID: "trc_empty",
		Output:  json.RawMessage(`{"message":"ok"}`),
	}

	result, err := pipeline.EvaluateBatch(context.Background(), trace, nil)
	if err != nil {
		t.Fatalf("EvaluateBatch returned error: %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(result.Results))
	}
}
