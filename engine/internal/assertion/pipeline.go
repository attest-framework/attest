package assertion

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// Pipeline evaluates batches of assertions against a trace.
type Pipeline struct {
	registry     *Registry
	historyStore *cache.HistoryStore
}

// NewPipeline creates a new assertion evaluation pipeline.
func NewPipeline(registry *Registry) *Pipeline {
	return &Pipeline{registry: registry}
}

// NewPipelineWithHistory creates a pipeline that uses the history store for dynamic threshold evaluation.
func NewPipelineWithHistory(registry *Registry, store *cache.HistoryStore) *Pipeline {
	return &Pipeline{registry: registry, historyStore: store}
}

// layerOrder defines evaluation order by assertion type.
var layerOrder = map[string]int{
	types.TypeSchema:     1,
	types.TypeConstraint: 2,
	types.TypeTrace:      3,
	types.TypeTraceTree:  3,
	types.TypeContent:    4,
	types.TypeEmbedding:  5,
	types.TypeLLMJudge:   6,
}

// EvaluateBatch evaluates all assertions against the trace in layer order.
// L1-4 (schema, constraint, trace, content) run sequentially. L5-6 (embedding, llm_judge)
// run concurrently after L1-4 completes. If any L1-4 assertion produces a hard_fail, L5-6 are skipped.
// Unknown assertion types produce a hard_fail result rather than aborting the batch.
// If a BudgetTracker is set on the pipeline, soft-fail budget enforcement is applied.
func (p *Pipeline) EvaluateBatch(trace *types.Trace, assertions []types.Assertion) (*BatchResult, error) {
	return p.EvaluateBatchWithBudget(trace, assertions, nil)
}

// EvaluateBatchWithBudget evaluates all assertions, applying budget tracking when budget is non-nil.
// If the soft-fail budget is exceeded, the batch stops and returns a BudgetExceededError.
// L1-4 assertions run sequentially; L5-6 fan out concurrently. Any L1-4 hard_fail gates L5-6.
func (p *Pipeline) EvaluateBatchWithBudget(trace *types.Trace, assertions []types.Assertion, budget *BudgetTracker) (*BatchResult, error) {
	sorted := make([]types.Assertion, len(assertions))
	copy(sorted, assertions)

	// Insertion sort — batch sizes are small and this avoids an import of sort.
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && layerOrder[sorted[j].Type] < layerOrder[sorted[j-1].Type]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	// Partition at L4/L5 boundary.
	splitIdx := len(sorted)
	for i, a := range sorted {
		if layerOrder[a.Type] >= 5 {
			splitIdx = i
			break
		}
	}
	l14, l56 := sorted[:splitIdx], sorted[splitIdx:]

	result := &BatchResult{
		Results: make([]types.AssertionResult, 0, len(sorted)),
	}

	// Phase 1: Evaluate L1-4 sequentially.
	hardFail := false
	for i := range l14 {
		eval, err := p.registry.Get(l14[i].Type)
		if err != nil {
			ar := types.AssertionResult{
				AssertionID: l14[i].AssertionID,
				Status:      types.StatusHardFail,
				Score:       0.0,
				Explanation: fmt.Sprintf("unknown assertion type: %s", l14[i].Type),
				RequestID:   l14[i].RequestID,
			}
			result.Results = append(result.Results, ar)
			hardFail = true
			if budget != nil {
				if budgetErr := budget.Record(&ar); budgetErr != nil {
					return result, budgetErr
				}
			}
			continue
		}

		ar := eval.Evaluate(trace, &l14[i])
		p.applyDynamicThreshold(ar, &l14[i])
		result.Results = append(result.Results, *ar)
		result.TotalCost += ar.Cost
		result.TotalDurationMS += ar.DurationMS

		if ar.Status == types.StatusHardFail {
			hardFail = true
		}

		if budget != nil {
			if budgetErr := budget.Record(ar); budgetErr != nil {
				return result, budgetErr
			}
		}
	}

	// Gate: skip L5-6 if any L1-4 hard failure.
	if hardFail || len(l56) == 0 {
		return result, nil
	}

	// Phase 2: Evaluate L5-6 concurrently.
	l56Results := make([]types.AssertionResult, len(l56))
	l56Costs := make([]float64, len(l56))
	l56Durations := make([]int64, len(l56))
	var wg sync.WaitGroup

	for i := range l56 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			eval, err := p.registry.Get(l56[idx].Type)
			if err != nil {
				l56Results[idx] = types.AssertionResult{
					AssertionID: l56[idx].AssertionID,
					Status:      types.StatusHardFail,
					Score:       0.0,
					Explanation: fmt.Sprintf("unknown assertion type: %s", l56[idx].Type),
					RequestID:   l56[idx].RequestID,
				}
				return
			}
			ar := eval.Evaluate(trace, &l56[idx])
			p.applyDynamicThreshold(ar, &l56[idx])
			l56Results[idx] = *ar
			l56Costs[idx] = ar.Cost
			l56Durations[idx] = ar.DurationMS
		}(i)
	}

	wg.Wait()

	// Merge L5-6 results in deterministic index order.
	for i := range l56Results {
		result.Results = append(result.Results, l56Results[i])
		result.TotalCost += l56Costs[i]
		result.TotalDurationMS += l56Durations[i]

		if budget != nil {
			if budgetErr := budget.Record(&l56Results[i]); budgetErr != nil {
				return result, budgetErr
			}
		}
	}

	return result, nil
}

// applyDynamicThreshold checks if the assertion spec contains "threshold":"dynamic"
// and if so, overrides the result status using ClassifyDynamic against stored history.
// No-ops when the historyStore is nil or the spec does not request dynamic classification.
func (p *Pipeline) applyDynamicThreshold(ar *types.AssertionResult, a *types.Assertion) {
	if p.historyStore == nil {
		return
	}

	var spec struct {
		Threshold string `json:"threshold"`
	}
	if err := json.Unmarshal(a.Spec, &spec); err != nil || spec.Threshold != "dynamic" {
		return
	}

	history, err := p.historyStore.QueryWindow(a.AssertionID, DefaultDynamicConfig.WindowSize)
	if err != nil {
		// Non-fatal: leave status unchanged.
		return
	}

	ar.Status = ClassifyDynamic(ar.Score, history, DefaultDynamicConfig)
}
