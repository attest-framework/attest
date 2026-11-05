package assertion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/encoding/json"

	"github.com/attest-framework/attest/engine/internal/cache"
	"github.com/attest-framework/attest/engine/pkg/types"
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
	types.TypeEffect:     3,
	types.TypeContent:    4,
	types.TypeEmbedding:  5,
	types.TypeLLMJudge:   6,
	types.TypePlugin:     8,
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
// If the soft-fail budget is exceeded, the batch still evaluates everything and
// sets BatchResult.BudgetExceeded; it is not an error.
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
	var plugins []types.Assertion
	l56 = slices.DeleteFunc(l56, func(a types.Assertion) bool {
		if a.Type != types.TypePlugin {
			return false
		}
		plugins = append(plugins, a)
		return true
	})

	result := &BatchResult{
		Results: make([]types.AssertionResult, 0, len(sorted)),
	}

	// Phase 1: Evaluate L1-4 sequentially.
	hardFail := false
	for i := range l14 {
		// A cancelled context (a call past its deadline) stops the batch: every
		// assertion not yet run is reported not evaluated, and the caller gets the
		// context error with the partial results. Reason short_circuited is the
		// closest member of the closed C1 set: the batch stopped before the
		// assertion ran. An uncancelled context changes nothing.
		if err := ctx.Err(); err != nil {
			rest := append(append([]types.Assertion{}, l14[i:]...), l56...)
			result.Results = appendNotEvaluated(result.Results, rest, types.ReasonShortCircuited,
				"not run: the evaluation was cancelled before this assertion ran")
			return result, err
		}
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
			result.noteBudget(budget, &ar)
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

		result.noteBudget(budget, ar)
	}

	// Plugin results were computed by the SDK and are free to report, so they
	// are neither gated by an L1-4 hard failure nor able to gate L5-6.
	for i := range plugins {
		eval, _ := p.registry.Get(plugins[i].Type)
		ar := eval.Evaluate(trace, &plugins[i])
		annotateDiagnostics(ar, &plugins[i])
		result.Results = append(result.Results, *ar)
		result.TotalDurationMS += ar.DurationMS
		result.noteBudget(budget, ar)
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

		result.noteBudget(budget, &l56Results[i])
	}

	return result, nil
}

// noteBudget records ar against the soft-fail budget. A breach sets
// BudgetExceeded and nothing else: the batch keeps evaluating and every row,
// hard failures included, is returned.
func (r *BatchResult) noteBudget(budget *BudgetTracker, ar *types.AssertionResult) {
	if budget == nil {
		return
	}
	var exceeded *BudgetExceededError
	if err := budget.Record(ar); errors.As(err, &exceeded) {
		r.BudgetExceeded = true
	}
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
// and if so, tightens the result against the C6 drift baseline of stored
// passing and soft scores. The bar is never below the static threshold, so
// a result that already fails keeps its status; a passing score under the bar
// becomes a failure (soft when the spec says soft). Either way the baseline
// goes into the explanation.
// Sets ThresholdSource to "static", "dynamic", or "dynamic_unavailable" so callers
// can distinguish the three classification regimes. When the historyStore lookup
// fails, the status keeps its static value, the source is marked
// "dynamic_unavailable", a prefix is prepended to Explanation, and a warning is
// logged. A not_evaluated result is never reclassified: nothing was measured,
// so it keeps its status and reason and gets no ThresholdSource.
func (p *Pipeline) applyDynamicThreshold(ar *types.AssertionResult, a *types.Assertion) {
	if ar.Status == types.StatusNotEvaluated {
		return
	}
	var spec struct {
		Threshold thresholdSpec `json:"threshold"`
		Soft      bool          `json:"soft"`
	}
	specErr := json.Unmarshal(a.Spec, &spec)
	if specErr != nil || !spec.Threshold.Dynamic {
		ar.ThresholdSource = types.ThresholdSourceStatic
		return
	}

	if p.historyStore == nil {
		ar.ThresholdSource = types.ThresholdSourceDynamicUnavailable
		ar.Explanation = "[dynamic_unavailable: history store not configured] " + ar.Explanation
		return
	}

	history, err := p.historyStore.QueryBaseline(a.AssertionID, DefaultDynamicConfig.WindowSize, false)
	if err != nil {
		slog.Warn("dynamic threshold unavailable",
			"assertion_id", a.AssertionID,
			"err", err)
		ar.ThresholdSource = types.ThresholdSourceDynamicUnavailable
		ar.Explanation = fmt.Sprintf("[dynamic_unavailable: %v] %s", err, ar.Explanation)
		return
	}

	ar.ThresholdSource = types.ThresholdSourceDynamic
	baseline := ComputeBaseline(history, DefaultThresholds.Pass, DefaultDynamicConfig)
	if !baseline.Active || ar.Score >= baseline.Bar {
		return
	}
	if ar.Status == types.StatusPass {
		ar.Status = types.StatusHardFail
		if spec.Soft {
			ar.Status = types.StatusSoftFail
		}
	}
	ar.Explanation = fmt.Sprintf(
		"dynamic threshold: score %.4f < bar %.4f (baseline of %d passing runs: mean %.4f, stddev %.4f; bar = max(static %.2f, mean - %.0f sigma, sigma floor %.2f)). %s",
		ar.Score, baseline.Bar, baseline.Count, baseline.Mean, baseline.Stddev,
		DefaultThresholds.Pass, DefaultDynamicConfig.SigmaScale, DefaultDynamicConfig.SigmaFloor, ar.Explanation)
}
