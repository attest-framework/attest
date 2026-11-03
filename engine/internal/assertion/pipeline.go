package assertion

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/encoding/json"

	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// DefaultEvalConcurrency is the maximum number of L5/L6 assertions evaluated
// in parallel when the pipeline is constructed without an explicit limit.
const DefaultEvalConcurrency = 8

// Pipeline evaluates batches of assertions against a trace.
type Pipeline struct {
	registry        *Registry
	historyStore    *cache.HistoryStore
	evalConcurrency int
	// simulation disables L5/L6: their assertions return
	// not_evaluated(simulation) without contacting any provider.
	simulation bool
}

// SimulationModeFromEnv reports whether ATTEST_SIMULATION requests
// simulation mode: "1", "true" or "yes", case-insensitive, surrounding
// whitespace ignored.
func SimulationModeFromEnv() bool {
	switch strings.TrimSpace(strings.ToLower(os.Getenv("ATTEST_SIMULATION"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// NewPipeline creates a new assertion evaluation pipeline with simulation
// mode off; the engine server enables it with SetSimulation.
func NewPipeline(registry *Registry) *Pipeline {
	return &Pipeline{registry: registry, evalConcurrency: DefaultEvalConcurrency}
}

// NewPipelineWithHistory creates a pipeline that uses the history store for dynamic threshold evaluation.
func NewPipelineWithHistory(registry *Registry, store *cache.HistoryStore) *Pipeline {
	return &Pipeline{registry: registry, historyStore: store, evalConcurrency: DefaultEvalConcurrency}
}

// SetSimulation enables or disables simulation mode. In simulation mode
// L1–L4 run normally and every L5/L6 assertion returns
// not_evaluated(simulation). Call before EvaluateBatch.
func (p *Pipeline) SetSimulation(on bool) {
	p.simulation = on
}

// SetEvalConcurrency overrides the L5/L6 worker pool size. Values <= 0 are clamped
// to DefaultEvalConcurrency. Call before EvaluateBatch — concurrent reconfiguration
// is not supported.
func (p *Pipeline) SetEvalConcurrency(n int) {
	if n <= 0 {
		n = DefaultEvalConcurrency
	}
	p.evalConcurrency = n
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
// run concurrently up to evalConcurrency goroutines after L1-4 completes. If any L1-4
// assertion produces a hard_fail, L5-6 return not_evaluated(short_circuited)
// rows; in simulation mode they return not_evaluated(simulation); without a
// registered provider they return not_evaluated(provider_not_configured). Unknown assertion types produce a
// hard_fail result rather than aborting the batch. If ctx is cancelled, in-flight L5-6
// goroutines short-circuit and the batch returns ctx.Err().
// If a BudgetTracker is set on the pipeline, soft-fail budget enforcement is applied.
func (p *Pipeline) EvaluateBatch(ctx context.Context, trace *types.Trace, assertions []types.Assertion) (*BatchResult, error) {
	return p.EvaluateBatchWithBudget(ctx, trace, assertions, nil)
}

// EvaluateBatchWithBudget evaluates all assertions, applying budget tracking when budget is non-nil.
// If the soft-fail budget is exceeded, the batch stops and returns a BudgetExceededError.
// L1-4 assertions run sequentially; L5-6 fan out concurrently bounded by evalConcurrency.
// Any L1-4 hard_fail gates L5-6. ctx cancellation is propagated to L5-6 workers.
func (p *Pipeline) EvaluateBatchWithBudget(ctx context.Context, trace *types.Trace, assertions []types.Assertion, budget *BudgetTracker) (*BatchResult, error) {
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
			annotateDiagnostics(&ar, &l14[i])
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
		annotateDiagnostics(ar, &l14[i])
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

	if len(l56) == 0 {
		return result, nil
	}

	// Gate: L5-6 do not run after an L1-4 hard failure or in simulation
	// mode. Every requested assertion still gets a result row.
	if hardFail {
		result.Results = appendNotEvaluated(result.Results, l56, types.ReasonShortCircuited,
			"not run: a deterministic assertion (layers 1–4) hard-failed in this batch")
		return result, nil
	}
	if p.simulation {
		result.Results = appendNotEvaluated(result.Results, l56, types.ReasonSimulation,
			"not run: simulation mode disables embedding and judge providers")
		return result, nil
	}

	// Phase 2: Evaluate L5-6 concurrently with a bounded worker pool.
	l56Results := make([]types.AssertionResult, len(l56))
	l56Costs := make([]float64, len(l56))
	l56Durations := make([]int64, len(l56))

	concurrency := p.evalConcurrency
	if concurrency <= 0 {
		concurrency = DefaultEvalConcurrency
	}
	if concurrency > len(l56) {
		concurrency = len(l56)
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i := range l56 {
		// Honor cancellation between dispatches; skip remaining assertions.
		if err := ctx.Err(); err != nil {
			break
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if err := ctx.Err(); err != nil {
			break
		}

		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			// Re-check cancellation inside the goroutine in case ctx fired
			// between dispatch and execution.
			if err := ctx.Err(); err != nil {
				ar := types.AssertionResult{
					AssertionID: l56[idx].AssertionID,
					Status:      types.StatusHardFail,
					Score:       0.0,
					Explanation: fmt.Sprintf("evaluation cancelled: %v", err),
					RequestID:   l56[idx].RequestID,
				}
				annotateDiagnostics(&ar, &l56[idx])
				l56Results[idx] = ar
				return
			}

			// L5/L6 types are always accepted; a missing evaluator means
			// the engine has no provider configured for that layer.
			eval, err := p.registry.Get(l56[idx].Type)
			if err != nil {
				ar := notEvaluatedResult(&l56[idx], time.Now(), types.ReasonProviderNotConfigured,
					fmt.Sprintf("not run: no %s provider is configured for the engine", l56[idx].Type))
				annotateDiagnostics(ar, &l56[idx])
				l56Results[idx] = *ar
				return
			}
			var ar *types.AssertionResult
			if ce, ok := eval.(ContextEvaluator); ok {
				ar = ce.EvaluateContext(ctx, trace, &l56[idx])
			} else {
				ar = eval.Evaluate(trace, &l56[idx])
			}
			p.applyDynamicThreshold(ar, &l56[idx])
			annotateDiagnostics(ar, &l56[idx])
			l56Results[idx] = *ar
			l56Costs[idx] = ar.Cost
			l56Durations[idx] = ar.DurationMS
		}(i)
	}

	wg.Wait()

	if err := ctx.Err(); err != nil {
		return result, err
	}

	// Merge L5-6 results in deterministic index order. Reaching this point
	// means dispatch finished without cancellation and every goroutine
	// populated its slot (cancelled goroutines still write a HardFail
	// result with AssertionID set).
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

// appendNotEvaluated appends one not_evaluated row per assertion with the
// given reason, so callers see every requested assertion in the results.
func appendNotEvaluated(out []types.AssertionResult, assertions []types.Assertion, reason, explanation string) []types.AssertionResult {
	now := time.Now()
	for i := range assertions {
		ar := notEvaluatedResult(&assertions[i], now, reason, explanation)
		annotateDiagnostics(ar, &assertions[i])
		out = append(out, *ar)
	}
	return out
}

// applyDynamicThreshold checks if the assertion spec contains "threshold":"dynamic"
// and if so, overrides the result status using ClassifyDynamic against stored history.
// Sets ThresholdSource to "static", "dynamic", or "dynamic_unavailable" so callers
// can distinguish the three classification regimes. When the historyStore lookup
// fails, the status keeps its static-fallback value, the source is marked
// "dynamic_unavailable", a prefix is prepended to Explanation, and a warning is
// logged. A not_evaluated result is never reclassified: nothing was measured,
// so it keeps its status and reason and gets no ThresholdSource.
func (p *Pipeline) applyDynamicThreshold(ar *types.AssertionResult, a *types.Assertion) {
	if ar.Status == types.StatusNotEvaluated {
		return
	}
	var spec struct {
		Threshold string `json:"threshold"`
	}
	specErr := json.Unmarshal(a.Spec, &spec)
	wantDynamic := specErr == nil && spec.Threshold == "dynamic"

	if !wantDynamic {
		ar.ThresholdSource = types.ThresholdSourceStatic
		return
	}

	if p.historyStore == nil {
		ar.ThresholdSource = types.ThresholdSourceDynamicUnavailable
		ar.Explanation = "[dynamic_unavailable: history store not configured] " + ar.Explanation
		return
	}

	history, err := p.historyStore.QueryWindow(a.AssertionID, DefaultDynamicConfig.WindowSize)
	if err != nil {
		slog.Warn("dynamic threshold unavailable",
			"assertion_id", a.AssertionID,
			"err", err)
		ar.ThresholdSource = types.ThresholdSourceDynamicUnavailable
		ar.Explanation = fmt.Sprintf("[dynamic_unavailable: %v] %s", err, ar.Explanation)
		return
	}

	ar.Status = ClassifyDynamic(ar.Score, history, DefaultDynamicConfig)
	ar.ThresholdSource = types.ThresholdSourceDynamic
}
