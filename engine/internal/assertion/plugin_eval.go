package assertion

import (
	"fmt"
	"sync"
	"time"

	"github.com/segmentio/encoding/json"

	"github.com/attest-framework/attest/engine/pkg/types"
)

// PluginResults holds the results SDKs submit with submit_plugin_result until
// the matching type "plugin" assertion is evaluated. Plugins run in the SDK
// process, so the engine never executes them: it reports what was submitted.
// A later submission for the same (trace_id, assertion_id) replaces the
// earlier one. Safe for concurrent use.
type PluginResults struct {
	mu      sync.Mutex
	results map[pluginKey]SubmittedPluginResult
}

type pluginKey struct{ traceID, assertionID string }

// SubmittedPluginResult is one accepted submit_plugin_result payload.
type SubmittedPluginResult struct {
	PluginName  string
	Status      string
	Score       float64
	Explanation string
}

// NewPluginResults returns an empty store.
func NewPluginResults() *PluginResults {
	return &PluginResults{results: make(map[pluginKey]SubmittedPluginResult)}
}

// ValidatePluginResult rejects a submission the engine cannot report: an
// unknown status or a score outside [0, 1].
func ValidatePluginResult(r types.PluginResult) error {
	switch r.Status {
	case types.StatusPass, types.StatusSoftFail, types.StatusHardFail:
	default:
		return fmt.Errorf("plugin result status %q is not one of pass, soft_fail, hard_fail", r.Status)
	}
	if r.Score < 0 || r.Score > 1 || r.Score != r.Score {
		return fmt.Errorf("plugin result score %v is outside [0, 1]", r.Score)
	}
	return nil
}

// Submit stores a result for the plugin assertion assertionID in trace traceID.
func (p *PluginResults) Submit(traceID, assertionID, pluginName string, r types.PluginResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.results[pluginKey{traceID, assertionID}] = SubmittedPluginResult{
		PluginName:  pluginName,
		Status:      r.Status,
		Score:       r.Score,
		Explanation: r.Explanation,
	}
}

func (p *PluginResults) get(traceID, assertionID string) (SubmittedPluginResult, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.results[pluginKey{traceID, assertionID}]
	return r, ok
}

// PluginEvaluator reports the result the SDK submitted for a type "plugin"
// assertion. With no submission the row is not_evaluated(missing_evidence), so
// an assertion that never ran cannot pass.
type PluginEvaluator struct {
	results *PluginResults
}

// NewPluginEvaluator returns an evaluator reading from results.
func NewPluginEvaluator(results *PluginResults) *PluginEvaluator {
	return &PluginEvaluator{results: results}
}

// Evaluate implements Evaluator.
func (e *PluginEvaluator) Evaluate(trace *types.Trace, a *types.Assertion) *types.AssertionResult {
	start := time.Now()
	var spec struct {
		PluginID string `json:"plugin_id"`
	}
	if err := json.Unmarshal(a.Spec, &spec); err != nil || spec.PluginID == "" {
		return &types.AssertionResult{
			AssertionID: a.AssertionID,
			Status:      types.StatusHardFail,
			Explanation: "plugin assertion spec needs a plugin_id string",
			RequestID:   a.RequestID,
			DurationMS:  time.Since(start).Milliseconds(),
		}
	}
	got, ok := e.results.get(trace.TraceID, a.AssertionID)
	if !ok {
		return notEvaluatedResult(a, start, types.ReasonMissingEvidence,
			fmt.Sprintf("no result was submitted for plugin %q: send submit_plugin_result before evaluate_batch", spec.PluginID))
	}
	if got.PluginName != spec.PluginID {
		return &types.AssertionResult{
			AssertionID: a.AssertionID,
			Status:      types.StatusHardFail,
			Explanation: fmt.Sprintf("plugin result came from %q but the assertion names plugin %q", got.PluginName, spec.PluginID),
			RequestID:   a.RequestID,
			DurationMS:  time.Since(start).Milliseconds(),
		}
	}
	return &types.AssertionResult{
		AssertionID: a.AssertionID,
		Status:      got.Status,
		Score:       got.Score,
		Explanation: got.Explanation,
		RequestID:   a.RequestID,
		DurationMS:  time.Since(start).Milliseconds(),
	}
}
