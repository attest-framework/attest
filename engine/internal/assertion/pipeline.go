package assertion

import (
	"fmt"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// Pipeline evaluates batches of assertions against a trace.
type Pipeline struct {
	registry *Registry
}

// NewPipeline creates a new assertion evaluation pipeline.
func NewPipeline(registry *Registry) *Pipeline {
	return &Pipeline{registry: registry}
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
// Assertions are evaluated in layer order: schema → constraint → trace → content → embedding → llm_judge.
// Unknown assertion types produce a hard_fail result rather than aborting the batch.
// If a BudgetTracker is set on the pipeline, soft-fail budget enforcement is applied.
func (p *Pipeline) EvaluateBatch(trace *types.Trace, assertions []types.Assertion) (*BatchResult, error) {
	return p.EvaluateBatchWithBudget(trace, assertions, nil)
}

// EvaluateBatchWithBudget evaluates all assertions, applying budget tracking when budget is non-nil.
// If the soft-fail budget is exceeded, the batch stops and returns a BudgetExceededError.
func (p *Pipeline) EvaluateBatchWithBudget(trace *types.Trace, assertions []types.Assertion, budget *BudgetTracker) (*BatchResult, error) {
	sorted := make([]types.Assertion, len(assertions))
	copy(sorted, assertions)

	// Insertion sort — batch sizes are small and this avoids an import of sort.
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && layerOrder[sorted[j].Type] < layerOrder[sorted[j-1].Type]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	result := &BatchResult{
		Results: make([]types.AssertionResult, 0, len(sorted)),
	}

	for i := range sorted {
		eval, err := p.registry.Get(sorted[i].Type)
		if err != nil {
			ar := types.AssertionResult{
				AssertionID: sorted[i].AssertionID,
				Status:      types.StatusHardFail,
				Score:       0.0,
				Explanation: fmt.Sprintf("unknown assertion type: %s", sorted[i].Type),
				RequestID:   sorted[i].RequestID,
			}
			result.Results = append(result.Results, ar)
			if budget != nil {
				if budgetErr := budget.Record(&ar); budgetErr != nil {
					return result, budgetErr
				}
			}
			continue
		}

		ar := eval.Evaluate(trace, &sorted[i])
		result.Results = append(result.Results, *ar)
		result.TotalCost += ar.Cost
		result.TotalDurationMS += ar.DurationMS

		if budget != nil {
			if budgetErr := budget.Record(ar); budgetErr != nil {
				return result, budgetErr
			}
		}
	}

	return result, nil
}
