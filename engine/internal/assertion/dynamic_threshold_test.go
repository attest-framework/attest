package assertion

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/attest-framework/attest/engine/internal/cache"
	"github.com/attest-framework/attest/engine/pkg/types"
)

func newDynamicHistory(t *testing.T) *cache.HistoryStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := cache.NewHistoryStore(db)
	if err != nil {
		t.Fatalf("NewHistoryStore: %v", err)
	}
	return store
}

func dynamicJudgePipeline(t *testing.T, store *cache.HistoryStore, reply func(user string) float64) *Pipeline {
	t.Helper()
	fj := newFakeJudge(t, func(_, user string) string {
		return fmt.Sprintf(`{"score": %v, "explanation": "fake verdict"}`, reply(user))
	})
	ev := evaluatorFor(t, fj.URL, nil)
	reg := NewRegistry(WithJudge(ev.provider, ev.rubrics, nil))
	return NewPipelineWithHistory(reg, store)
}

func dynamicJudgeAssertion(id, criteria string) types.Assertion {
	return types.Assertion{
		AssertionID: id,
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(fmt.Sprintf(`{"target":"output","criteria":%q,"threshold":"dynamic"}`, criteria)),
	}
}

func dynamicTrace(n int) *types.Trace {
	return &types.Trace{TraceID: fmt.Sprintf("trc_%d", n), Output: json.RawMessage(`"the agent output"`)}
}

func TestDynamicJudge_TenAt09ThenHalf_HardFailsWithDriftDetail(t *testing.T) {
	// Arrange
	store := newDynamicHistory(t)
	var score atomic.Value
	score.Store(0.9)
	p := dynamicJudgePipeline(t, store, func(string) float64 { return score.Load().(float64) })
	a := dynamicJudgeAssertion("judge-dyn", "is it helpful")
	run := func(n int) types.AssertionResult {
		res, err := p.EvaluateBatch(context.Background(), dynamicTrace(n), []types.Assertion{a})
		if err != nil {
			t.Fatalf("EvaluateBatch: %v", err)
		}
		ar := res.Results[0]
		if err := store.Record(fmt.Sprintf("trc_%d", n), ar.AssertionID, ar.Type, ar.Score, ar.Status); err != nil {
			t.Fatalf("Record: %v", err)
		}
		return ar
	}
	for i := 0; i < 10; i++ {
		if ar := run(i); ar.Status != types.StatusPass {
			t.Fatalf("run %d: status = %q (%s), want pass", i, ar.Status, ar.Explanation)
		}
	}

	// Act
	score.Store(0.5)
	got := run(10)

	// Assert
	if got.Status != types.StatusHardFail {
		t.Fatalf("status = %q, want hard_fail; explanation: %s", got.Status, got.Explanation)
	}
	if got.ThresholdSource != types.ThresholdSourceDynamic {
		t.Errorf("ThresholdSource = %q, want dynamic", got.ThresholdSource)
	}
	if !strings.Contains(got.Explanation, "dynamic threshold") {
		t.Errorf("explanation lacks drift detail: %q", got.Explanation)
	}
}

func TestDynamicJudge_TenAt1ThenPoint99_Passes(t *testing.T) {
	// Arrange
	store := newDynamicHistory(t)
	var score atomic.Value
	score.Store(1.0)
	p := dynamicJudgePipeline(t, store, func(string) float64 { return score.Load().(float64) })
	a := dynamicJudgeAssertion("judge-dyn", "is it helpful")
	for i := 0; i < 10; i++ {
		res, err := p.EvaluateBatch(context.Background(), dynamicTrace(i), []types.Assertion{a})
		if err != nil {
			t.Fatalf("EvaluateBatch: %v", err)
		}
		ar := res.Results[0]
		if err := store.Record(dynamicTrace(i).TraceID, ar.AssertionID, ar.Type, ar.Score, ar.Status); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	// Act
	score.Store(0.99)
	res, err := p.EvaluateBatch(context.Background(), dynamicTrace(10), []types.Assertion{a})
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}

	// Assert
	if got := res.Results[0]; got.Status != types.StatusPass || got.Score != 0.99 {
		t.Fatalf("status = %q score = %v, want pass at 0.99; explanation: %s", got.Status, got.Score, got.Explanation)
	}
}

func TestDynamicSimilarTo_TenAt09ThenHalf_HardFailsAndTenAt1ThenPoint99Passes(t *testing.T) {
	cases := []struct {
		name     string
		baseline float64
		latest   float64
		want     string
	}{
		{"drop below baseline hard-fails", 0.9, 0.5, types.StatusHardFail},
		{"tiny dip within sigma floor passes", 1.0, 0.99, types.StatusPass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			store := newDynamicHistory(t)
			emb := &mockEmbedder{model: "fake-embed", vectors: map[string][]float32{}}
			p := NewPipelineWithHistory(NewRegistry(WithEmbedding(emb, nil)), store)
			a := types.Assertion{
				AssertionID: "sim-dyn",
				Type:        types.TypeEmbedding,
				Spec:        json.RawMessage(`{"target":"output","reference":"ref text","threshold":"dynamic"}`),
			}
			setSimilarity := func(sim float64) {
				emb.vectors["ref text"] = []float32{1, 0, 0}
				emb.vectors["the agent output"] = cosineVector(sim)
			}
			run := func(n int, sim float64) types.AssertionResult {
				setSimilarity(sim)
				res, err := p.EvaluateBatch(context.Background(), dynamicTrace(n), []types.Assertion{a})
				if err != nil {
					t.Fatalf("EvaluateBatch: %v", err)
				}
				ar := res.Results[0]
				if err := store.Record(dynamicTrace(n).TraceID, ar.AssertionID, ar.Type, ar.Score, ar.Status); err != nil {
					t.Fatalf("Record: %v", err)
				}
				return ar
			}
			for i := 0; i < 10; i++ {
				if ar := run(i, tc.baseline); ar.Status != types.StatusPass {
					t.Fatalf("baseline run %d: status = %q (%s), want pass", i, ar.Status, ar.Explanation)
				}
			}

			// Act
			got := run(10, tc.latest)

			// Assert
			if got.Status != tc.want || math.Abs(got.Score-tc.latest) > 1e-4 {
				t.Fatalf("status = %q score = %v, want %q at %v; explanation: %s",
					got.Status, got.Score, tc.want, tc.latest, got.Explanation)
			}
		})
	}
}

func TestDynamicJudge_AllZeroHistory_ZeroScoreFailsAgainstStaticThreshold(t *testing.T) {
	for _, seededStatus := range []string{types.StatusPass, types.StatusHardFail} {
		t.Run("history rows recorded as "+seededStatus, func(t *testing.T) {
			// Arrange
			store := newDynamicHistory(t)
			for i := 0; i < 12; i++ {
				if err := store.Record(fmt.Sprintf("old_%d", i), "judge-dyn", types.TypeLLMJudge, 0, seededStatus); err != nil {
					t.Fatalf("Record: %v", err)
				}
			}
			p := dynamicJudgePipeline(t, store, func(string) float64 { return 0.0 })
			a := dynamicJudgeAssertion("judge-dyn", "is it helpful")

			// Act
			res, err := p.EvaluateBatch(context.Background(), dynamicTrace(0), []types.Assertion{a})
			if err != nil {
				t.Fatalf("EvaluateBatch: %v", err)
			}

			// Assert
			if got := res.Results[0]; got.Status != types.StatusHardFail {
				t.Fatalf("status = %q, want hard_fail; explanation: %s", got.Status, got.Explanation)
			}
		})
	}
}

func TestDynamicJudge_ReorderedAssertions_KeepEachBaseline(t *testing.T) {
	// Arrange: alpha scores 1.0 and beta 0.9 for ten runs, then each dips
	// within its own tolerance (alpha 0.99, beta 0.89).
	store := newDynamicHistory(t)
	var dip atomic.Bool
	p := dynamicJudgePipeline(t, store, func(user string) float64 {
		alphaScore, betaScore := 1.0, 0.9
		if dip.Load() {
			alphaScore, betaScore = 0.99, 0.89
		}
		if strings.Contains(user, "alpha") {
			return alphaScore
		}
		return betaScore
	})
	alpha := dynamicJudgeAssertion("id-alpha", "alpha criteria")
	beta := dynamicJudgeAssertion("id-beta", "beta criteria")
	record := func(n int, res *BatchResult) {
		for _, ar := range res.Results {
			if err := store.Record(dynamicTrace(n).TraceID, ar.AssertionID, ar.Type, ar.Score, ar.Status); err != nil {
				t.Fatalf("Record: %v", err)
			}
		}
	}
	for i := 0; i < 10; i++ {
		res, err := p.EvaluateBatch(context.Background(), dynamicTrace(i), []types.Assertion{alpha, beta})
		if err != nil {
			t.Fatalf("EvaluateBatch: %v", err)
		}
		record(i, res)
	}

	// Act: the same assertions in swapped order.
	dip.Store(true)
	res, err := p.EvaluateBatch(context.Background(), dynamicTrace(10), []types.Assertion{beta, alpha})
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}

	// Assert
	for _, ar := range res.Results {
		if ar.Status != types.StatusPass || ar.ThresholdSource != types.ThresholdSourceDynamic || ar.Score < 0.8 {
			t.Errorf("%s: status=%q source=%q (%s), want pass against its own baseline",
				ar.AssertionID, ar.Status, ar.ThresholdSource, ar.Explanation)
		}
	}
}

func cosineVector(sim float64) []float32 {
	rest := 1 - sim*sim
	if rest < 0 {
		rest = 0
	}
	return []float32{float32(sim), float32(math.Sqrt(rest)), 0}
}
