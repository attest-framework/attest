package assertion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/encoding/json"

	"github.com/attest-framework/attest/engine/internal/assertion/judge"
	"github.com/attest-framework/attest/engine/internal/cache"
	"github.com/attest-framework/attest/engine/internal/llm"
	"github.com/attest-framework/attest/engine/pkg/types"
)

// JudgeEvaluator implements Layer 6: LLM-based judge assertions.
type JudgeEvaluator struct {
	provider llm.Provider
	rubrics  *judge.RubricRegistry
	cache    *cache.JudgeCache
}

// NewJudgeEvaluator creates an evaluator using the given LLM provider, rubric registry, and optional cache.
// cache may be nil to disable caching.
func NewJudgeEvaluator(provider llm.Provider, rubrics *judge.RubricRegistry, c *cache.JudgeCache) *JudgeEvaluator {
	return &JudgeEvaluator{provider: provider, rubrics: rubrics, cache: c}
}

// judgeSpec is the expected structure of the assertion spec JSON.
//
// Repeat controls calibrated-judge sampling: with Repeat=N>1 the engine runs
// the judge N times, returns the median, and emits SampleScores plus mean and
// stddev. Cost is N × per-run cost. MetaEval is a legacy alias for
// Repeat=metaEvalRuns kept for backward compatibility — when both are set,
// Repeat wins.
type judgeSpec struct {
	Target     string        `json:"target"`
	Criteria   string        `json:"criteria"`
	Rubric     string        `json:"rubric"`
	Threshold  thresholdSpec `json:"threshold"`
	Soft       bool          `json:"soft"`
	Model      string        `json:"model"`
	MetaEval   bool          `json:"meta_eval"`
	Repeat     int           `json:"repeat"`
	BiasProbes []string      `json:"bias_probes"`
}

const (
	metaEvalRuns              = 3
	metaEvalTemperature       = 0.3
	metaEvalVarianceThreshold = 0.2
	maxRepeatRuns             = 16
)

// Evaluate runs the LLM judge assertion against the trace.
func (e *JudgeEvaluator) Evaluate(trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	return e.EvaluateContext(context.Background(), trace, assertion)
}

// EvaluateContext runs the judge under parent, which carries cancellation
// and the batch budget scope (WithBudgetScope).
func (e *JudgeEvaluator) EvaluateContext(parent context.Context, trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	start := time.Now()

	var spec judgeSpec
	if err := json.Unmarshal(assertion.Spec, &spec); err != nil {
		return specErrorResult(assertion, start, fmt.Sprintf("invalid judge spec: %v", err))
	}
	if spec.Target == "" {
		return specErrorResult(assertion, start, "judge spec missing required field: target")
	}
	rubricName := spec.Rubric
	if rubricName == "" {
		rubricName = "default"
	}
	if spec.Threshold.Value <= 0 {
		spec.Threshold.Value = 0.8 // also the floor of a dynamic bar
	}
	if spec.Threshold.Value > 1 {
		return specErrorResult(assertion, start, fmt.Sprintf("judge spec: threshold=%v out of range (0, 1]", spec.Threshold.Value))
	}

	rubric, err := e.rubrics.Get(rubricName)
	if err != nil {
		return specErrorResult(assertion, start, fmt.Sprintf("rubric not found: %v", err))
	}

	// The whole spec is validated before the cache is consulted, so a cache
	// hit cannot accept a spec a miss would reject.
	runs, err := repeatRuns(spec)
	if err != nil {
		return specErrorResult(assertion, start, err.Error())
	}
	probes, err := resolveBiasProbes(spec.BiasProbes)
	if err != nil {
		return specErrorResult(assertion, start, err.Error())
	}

	targetStr, err := ResolveTargetString(trace, spec.Target)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("target resolution failed: %v", err))
	}

	model := spec.Model
	if model == "" {
		model = e.provider.DefaultModel()
	}

	call := newJudgeCall(spec.Criteria, targetStr, rubric.Version, runs, probes)
	if e.cache != nil {
		if cached, cErr := e.cache.Get(call.cacheKey, rubricName, model); cErr == nil && cached != nil {
			durationMS := time.Since(start).Milliseconds()
			meta := &types.JudgeMetadata{
				Provider:      e.provider.Name(),
				Model:         model,
				RubricName:    rubricName,
				RubricVersion: rubric.Version,
				PromptHash:    judge.PromptHash(call.canonical),
				SampleScores:  []float64{cached.Score},
				ScoreMean:     cached.Score,
			}
			return e.buildResult(assertion, cached.Score, cached.Explanation, spec.Threshold.Value, spec.Soft, durationMS, 0,
				judgeBuildArgs{target: spec.Target, model: model, rubric: rubricName, meta: meta})
		}
	}

	timeoutSecs := judgeTimeoutSeconds()
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeoutSecs)*time.Second)
	defer cancel()

	if runs > 1 {
		return e.evaluateRepeated(ctx, assertion, rubric, model, call, spec, start, targetStr, rubricName, runs, probes)
	}

	return e.evaluateSinglePass(ctx, assertion, rubric, model, call, spec, start, targetStr, rubricName, probes)
}

// judgeCall is the prompt for one assertion evaluation.
type judgeCall struct {
	// criteria and target build the user message; each provider request frames
	// it with its own nonce (see fresh).
	criteria, target string
	// canonical is the same message with the fixed delimiters: it is the same
	// on every call, so it feeds the prompt hash.
	canonical string
	// cacheKey identifies the result a cache may serve: the canonical content,
	// the rubric version, the number of samples and the bias probes, because a
	// result computed without them is not a result computed with them.
	cacheKey string
}

func (c judgeCall) withCriteria(wrapped string) string {
	if c.criteria == "" {
		return wrapped
	}
	return fmt.Sprintf("Evaluation criteria: %s\n\n%s", c.criteria, wrapped)
}

// fresh returns a new Frame and the user message framed with its nonce. Every
// provider request (each repeat sample and each bias probe) takes its own, so
// a nonce leaked in one reply protects nothing about the next request.
func (c judgeCall) fresh() (judge.Frame, string, error) {
	frame, err := judge.NewFrame()
	if err != nil {
		return judge.Frame{}, "", err
	}
	return frame, c.withCriteria(frame.Wrap(c.target)), nil
}

func newJudgeCall(criteria, target, rubricVersion string, runs int, probes []string) judgeCall {
	call := judgeCall{criteria: criteria, target: target}
	canonical := call.withCriteria(judge.WrapAgentOutput(target))
	key := fmt.Sprintf("%s\x00repeat=%d\x00probes=%s", canonical, runs, strings.Join(probes, ","))
	call.canonical = canonical
	call.cacheKey = cache.JudgePromptKey(rubricVersion, key)
	return call
}

// specErrorResult fails an assertion whose spec cannot be evaluated and
// labels it spec_error: the fault is in the test, not in the agent.
func specErrorResult(assertion *types.Assertion, start time.Time, explanation string) *types.AssertionResult {
	r := failResult(assertion, start, explanation)
	r.FailureClass = types.FailureClassSpecError
	return r
}

// judgeBuildArgs bundles the optional diagnostic inputs to buildResult so
// the function signature does not balloon past four positional parameters.
type judgeBuildArgs struct {
	target string
	model  string
	rubric string
	meta   *types.JudgeMetadata
}

func (e *JudgeEvaluator) buildResult(
	assertion *types.Assertion,
	score float64,
	explanation string,
	threshold float64,
	soft bool,
	durationMS int64,
	cost float64,
	diag judgeBuildArgs,
) *types.AssertionResult {
	status := types.StatusPass
	if score < threshold {
		if soft {
			status = types.StatusSoftFail
		} else {
			status = types.StatusHardFail
		}
	}

	expected := fmt.Sprintf("judge_score >= %.2f against rubric %q", threshold, diag.rubric)
	actual := fmt.Sprintf("judge_score=%.2f, model=%s, rationale=%s", score, diag.model, truncate(explanation))

	suggestion := ""
	if status != types.StatusPass {
		suggestion = "Calibrate the judge: check rubric clarity, sample human labels, or raise the threshold only if false-positives matter more than false-negatives."
	}

	result := &types.AssertionResult{
		AssertionID:     assertion.AssertionID,
		Type:            types.TypeLLMJudge,
		Status:          status,
		Score:           score,
		Explanation:     explanation,
		Cost:            cost,
		DurationMS:      durationMS,
		RequestID:       assertion.RequestID,
		TraceNodePath:   diag.target,
		Expected:        expected,
		Actual:          actual,
		SuggestedAction: suggestion,
		Judge:           diag.meta,
	}
	// Classify against this assertion's own threshold, not the global 0.5.
	result.FailureClass = types.ClassifyFailureAt(result, threshold)
	return result
}

// scoreVarianceStats computes mean/stddev for a slice of scores. Returns
// zeros when scores is empty.
func scoreVarianceStats(scores []float64) (mean, stddev float64) {
	if len(scores) == 0 {
		return 0, 0
	}
	for _, s := range scores {
		mean += s
	}
	mean /= float64(len(scores))
	if len(scores) == 1 {
		return mean, 0
	}
	for _, s := range scores {
		stddev += (s - mean) * (s - mean)
	}
	stddev = math.Sqrt(stddev / float64(len(scores)-1))
	return mean, stddev
}

// judgeTimeoutSeconds reads the judge evaluation timeout from ATTEST_JUDGE_TIMEOUT_S.
// Defaults to 30 seconds if unset or invalid.
// JudgeTimeout is the per-evaluation judge timeout (ATTEST_JUDGE_TIMEOUT_S,
// default 30s). Providers without their own HTTP timeout, such as the ACP
// agent, also bound each call with it.
func JudgeTimeout() time.Duration {
	return time.Duration(judgeTimeoutSeconds()) * time.Second
}

func judgeTimeoutSeconds() int {
	v := os.Getenv("ATTEST_JUDGE_TIMEOUT_S")
	if v == "" {
		return 30
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 30
	}
	return n
}

// repeatRuns resolves the requested number of judge samples from the spec
// or env. Precedence: spec.Repeat > spec.MetaEval > ATTEST_JUDGE_META_EVAL.
// Returns (1, nil) for single-pass. An explicit out-of-range Repeat (≤0 or
// > maxRepeatRuns) is rejected so config errors fail fast rather than
// silently degrading to single-pass.
func repeatRuns(spec judgeSpec) (int, error) {
	if spec.Repeat != 0 {
		if spec.Repeat < 1 || spec.Repeat > maxRepeatRuns {
			return 0, fmt.Errorf("judge spec: repeat=%d out of range [1, %d]", spec.Repeat, maxRepeatRuns)
		}
		return spec.Repeat, nil
	}
	if spec.MetaEval || os.Getenv("ATTEST_JUDGE_META_EVAL") == "true" {
		return metaEvalRuns, nil
	}
	return 1, nil
}

// evaluateSinglePass runs the judge once (default behavior). When probes are
// non-empty, the engine runs an additional judge call per probe and folds the
// resulting deltas into JudgeMetadata.BiasProbes; cost is summed across all
// calls.
func (e *JudgeEvaluator) evaluateSinglePass(
	ctx context.Context,
	assertion *types.Assertion,
	rubric *judge.Rubric,
	model string,
	call judgeCall,
	spec judgeSpec,
	start time.Time,
	targetStr, rubricName string,
	probes []string,
) *types.AssertionResult {
	frame, content, err := call.fresh()
	if err != nil {
		return notEvaluatedResult(assertion, start, types.ReasonProviderUnavailable, err.Error())
	}
	req := &llm.CompletionRequest{
		Model:        model,
		SystemPrompt: frame.Prompt(rubric),
		Messages:     []llm.Message{{Role: "user", Content: content}},
		Temperature:  0.0,
		MaxTokens:    256,
	}

	resp, err := e.provider.Complete(ctx, req)
	if err != nil {
		return judgeCallFailed(assertion, start, err)
	}

	scoreResult, err := frame.ParseScoreResult(resp.Content)
	if err != nil {
		// The call was billed even though its output is unusable.
		ar := notEvaluatedResult(assertion, start, types.ReasonJudgeMalformed, fmt.Sprintf("judge response unusable: %v", err))
		ar.Cost = resp.Cost
		return ar
	}
	if scoreResult.Abstain {
		ar := notEvaluatedResult(assertion, start, types.ReasonJudgeAbstained, abstainExplanation(scoreResult.Reason))
		ar.Cost = resp.Cost
		ar.Judge = &types.JudgeMetadata{
			Provider:      e.provider.Name(),
			Model:         model,
			RubricName:    rubricName,
			RubricVersion: rubric.Version,
			PromptHash:    judge.PromptHash(call.canonical),
		}
		return ar
	}

	durationMS := time.Since(start).Milliseconds()

	if e.cache != nil {
		if putErr := e.cache.Put(call.cacheKey, rubricName, model, &cache.JudgeCacheEntry{
			Score:       scoreResult.Score,
			Explanation: scoreResult.Explanation,
		}); putErr != nil {
			slog.Error("judge cache write error", "err", putErr)
		}
	}

	probeResults, probeCost := runBiasProbes(ctx, e.provider, rubric, model, call, scoreResult.Score, probes)
	totalCost := resp.Cost + probeCost
	meta := &types.JudgeMetadata{
		Provider:      e.provider.Name(),
		Model:         model,
		RubricName:    rubricName,
		RubricVersion: rubric.Version,
		PromptHash:    judge.PromptHash(call.canonical),
		SampleScores:  []float64{scoreResult.Score},
		ScoreMean:     scoreResult.Score,
		BiasProbes:    probeResults,
	}
	durationMS = time.Since(start).Milliseconds()
	return e.buildResult(assertion, scoreResult.Score, scoreResult.Explanation, spec.Threshold.Value, spec.Soft, durationMS, totalCost,
		judgeBuildArgs{target: spec.Target, model: model, rubric: rubricName, meta: meta})
}

// metaEvalResult holds one judge run's output.
type metaEvalResult struct {
	score       float64
	explanation string
	cost        float64
	err         error
	// abstain is set when the judge declined to score this run.
	abstain       bool
	abstainReason string
}

// evaluateRepeated runs the judge `runs` times concurrently, takes the median
// score, and flags high variance in the explanation. Use to surface judge
// stability — sample stddev and per-sample scores are returned in
// JudgeMetadata so calibration tooling can compute agreement metrics.
func (e *JudgeEvaluator) evaluateRepeated(
	ctx context.Context,
	assertion *types.Assertion,
	rubric *judge.Rubric,
	model string,
	call judgeCall,
	spec judgeSpec,
	start time.Time,
	targetStr, rubricName string,
	runs int,
	probes []string,
) *types.AssertionResult {
	results := make([]metaEvalResult, runs)
	var wg sync.WaitGroup
	// A rejected credential fails every run alike: stop the runs not yet
	// sent instead of paying for each one.
	runCtx, cancelRuns := context.WithCancel(ctx)
	defer cancelRuns()

	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			frame, content, err := call.fresh()
			if err != nil {
				results[idx] = metaEvalResult{err: err}
				return
			}
			req := &llm.CompletionRequest{
				Model:        model,
				SystemPrompt: frame.Prompt(rubric),
				Messages:     []llm.Message{{Role: "user", Content: content}},
				Temperature:  metaEvalTemperature,
				MaxTokens:    256,
			}

			resp, err := e.provider.Complete(runCtx, req)
			if err != nil {
				if errors.Is(err, llm.ErrAuth) {
					cancelRuns()
				}
				results[idx] = metaEvalResult{err: err}
				return
			}

			sr, err := frame.ParseScoreResult(resp.Content)
			if err != nil {
				results[idx] = metaEvalResult{err: err, cost: resp.Cost}
				return
			}
			if sr.Abstain {
				results[idx] = metaEvalResult{abstain: true, abstainReason: sr.Reason, cost: resp.Cost}
				return
			}

			results[idx] = metaEvalResult{
				score:       sr.Score,
				explanation: sr.Explanation,
				cost:        resp.Cost,
			}
		}(i)
	}

	wg.Wait()

	// Collect successful results
	var scores []float64
	var explanations []string
	var totalCost float64
	var miss unscoredRuns
	for i, r := range results {
		totalCost += r.cost
		if miss.add(r) {
			continue
		}
		scores = append(scores, r.score)
		explanations = append(explanations, fmt.Sprintf("Run %d: %s", i+1, r.explanation))
	}

	// Need at least 1 scored run
	if len(scores) == 0 {
		ar := notEvaluatedResult(assertion, start, miss.reason(), fmt.Sprintf("none of %d judge runs scored: %s", runs, miss.summary()))
		ar.Cost = totalCost
		return ar
	}

	// Sort and take median
	sort.Float64s(scores)
	medianScore := median(scores)

	// Calculate variance (spread)
	spread := scores[len(scores)-1] - scores[0]
	var varianceNote string
	if spread > metaEvalVarianceThreshold {
		varianceNote = fmt.Sprintf(" [HIGH VARIANCE: spread=%.2f across %d runs]", spread, len(scores))
	}

	combinedExplanation := strings.Join(explanations, " | ") + " | Median selected." + varianceNote
	if miss.total() > 0 {
		combinedExplanation += fmt.Sprintf(" [%d of %d runs not scored: %s]", miss.total(), runs, miss.summary())
	}

	durationMS := time.Since(start).Milliseconds()

	// Cache the median result only when every run scored: a partial
	// sample must not stand in for N runs later.
	if e.cache != nil && miss.total() == 0 {
		if putErr := e.cache.Put(call.cacheKey, rubricName, model, &cache.JudgeCacheEntry{
			Score:       medianScore,
			Explanation: combinedExplanation,
		}); putErr != nil {
			slog.Error("judge cache write error", "err", putErr)
		}
	}

	probeResults, probeCost := runBiasProbes(ctx, e.provider, rubric, model, call, medianScore, probes)
	totalCost += probeCost

	mean, stddev := scoreVarianceStats(scores)
	meta := &types.JudgeMetadata{
		Provider:         e.provider.Name(),
		Model:            model,
		RubricName:       rubricName,
		RubricVersion:    rubric.Version,
		PromptHash:       judge.PromptHash(call.canonical),
		SampleScores:     scores,
		ScoreMean:        mean,
		ScoreStddev:      stddev,
		HighVarianceFlag: spread > metaEvalVarianceThreshold,
		BiasProbes:       probeResults,
	}
	durationMS = time.Since(start).Milliseconds()
	return e.buildResult(assertion, medianScore, combinedExplanation, spec.Threshold.Value, spec.Soft, durationMS, totalCost,
		judgeBuildArgs{target: spec.Target, model: model, rubric: rubricName, meta: meta})
}

// judgeCallFailed maps a failed judge call to not_evaluated:
// budget_exhausted when the budget refused it, otherwise provider_unavailable.
func judgeCallFailed(assertion *types.Assertion, start time.Time, err error) *types.AssertionResult {
	if errors.Is(err, llm.ErrBudgetExhausted) {
		return notEvaluatedResult(assertion, start, types.ReasonBudgetExhausted, fmt.Sprintf("judge call not sent: %v", err))
	}
	if errors.Is(err, llm.ErrAuth) {
		return notEvaluatedResult(assertion, start, types.ReasonProviderAuth, fmt.Sprintf("judge provider rejected the credentials: %v", err))
	}
	return notEvaluatedResult(assertion, start, types.ReasonProviderUnavailable, fmt.Sprintf("judge provider call failed: %v", err))
}

// abstainExplanation renders the judge's abstain reason, which is model
// output, with control and format characters removed and its length cut.
func abstainExplanation(reason string) string {
	reason = strings.TrimSpace(llm.StripControl(reason))
	if reason == "" {
		return "judge abstained without giving a reason"
	}
	return truncate("judge abstained: " + reason)
}

// unscoredRuns counts repeated judge runs that produced no score, by cause.
type unscoredRuns struct {
	failed, refused, abstained, malformed, authFailed int
	firstErr                                          error
	firstAbstain                                      string
}

// add records r when it produced no score and reports whether it did not.
func (u *unscoredRuns) add(r metaEvalResult) bool {
	switch {
	case r.abstain:
		u.abstained++
		if u.firstAbstain == "" {
			u.firstAbstain = r.abstainReason
		}
	case r.err != nil && errors.Is(r.err, llm.ErrBudgetExhausted):
		u.refused++
	case r.err != nil && errors.Is(r.err, llm.ErrAuth):
		u.authFailed++
		if u.firstErr == nil {
			u.firstErr = r.err
		}
	case r.err != nil && errors.Is(r.err, judge.ErrMalformed):
		u.malformed++
		if u.firstErr == nil {
			u.firstErr = r.err
		}
	case r.err != nil:
		u.failed++
		if u.firstErr == nil {
			u.firstErr = r.err
		}
	default:
		return false
	}
	return true
}

func (u *unscoredRuns) total() int {
	return u.failed + u.refused + u.abstained + u.malformed + u.authFailed
}

// reason picks the not_evaluated reason when no run scored: a provider
// rejection outranks other provider failures, which outrank a malformed
// reply, which outranks an abstention, which outranks a budget refusal.
func (u *unscoredRuns) reason() string {
	switch {
	case u.authFailed > 0:
		return types.ReasonProviderAuth
	case u.failed > 0:
		return types.ReasonProviderUnavailable
	case u.malformed > 0:
		return types.ReasonJudgeMalformed
	case u.abstained > 0:
		return types.ReasonJudgeAbstained
	default:
		return types.ReasonBudgetExhausted
	}
}

func (u *unscoredRuns) summary() string {
	var parts []string
	if n := u.failed + u.malformed + u.authFailed; n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed (%v)", n, u.firstErr))
	}
	if u.abstained > 0 {
		parts = append(parts, fmt.Sprintf("%d abstained (%s)", u.abstained, truncate(llm.StripControl(u.firstAbstain))))
	}
	if u.refused > 0 {
		parts = append(parts, fmt.Sprintf("%d not sent: budget exhausted", u.refused))
	}
	return strings.Join(parts, ", ")
}

// median returns the median of sorted scores: the middle score, or the
// mean of the two middle scores for an even count.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
