package assertion

import (
	"context"
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

	"github.com/attest-ai/attest/engine/internal/assertion/judge"
	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/pkg/types"
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
	Target     string   `json:"target"`
	Criteria   string   `json:"criteria"`
	Rubric     string   `json:"rubric"`
	Threshold  float64  `json:"threshold"`
	Soft       bool     `json:"soft"`
	Model      string   `json:"model"`
	MetaEval   bool     `json:"meta_eval"`
	Repeat     int      `json:"repeat"`
	BiasProbes []string `json:"bias_probes"`
}

const (
	metaEvalRuns              = 3
	metaEvalTemperature       = 0.3
	metaEvalVarianceThreshold = 0.2
	maxRepeatRuns             = 16
)

// Evaluate runs the LLM judge assertion against the trace.
func (e *JudgeEvaluator) Evaluate(trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	start := time.Now()

	var spec judgeSpec
	if err := json.Unmarshal(assertion.Spec, &spec); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid judge spec: %v", err))
	}
	if spec.Target == "" {
		return failResult(assertion, start, "judge spec missing required field: target")
	}
	rubricName := spec.Rubric
	if rubricName == "" {
		rubricName = "default"
	}
	if spec.Threshold <= 0 {
		spec.Threshold = 0.8
	}

	rubric, err := e.rubrics.Get(rubricName)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("rubric not found: %v", err))
	}

	targetStr, err := ResolveTargetString(trace, spec.Target)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("target resolution failed: %v", err))
	}

	model := spec.Model
	if model == "" {
		model = e.provider.DefaultModel()
	}

	// Check cache
	if e.cache != nil {
		contentHash := cache.JudgeContentHash(targetStr)
		if cached, cErr := e.cache.Get(contentHash, rubricName, model); cErr == nil && cached != nil {
			durationMS := time.Since(start).Milliseconds()
			meta := &types.JudgeMetadata{
				Model:         model,
				RubricName:    rubricName,
				RubricVersion: rubric.Version,
				PromptHash:    judge.PromptHash(targetStr),
				SampleScores:  []float64{cached.Score},
				ScoreMean:     cached.Score,
			}
			return e.buildResult(assertion, cached.Score, cached.Explanation, spec.Threshold, spec.Soft, durationMS, 0,
				judgeBuildArgs{target: spec.Target, model: model, rubric: rubricName, meta: meta})
		}
	}

	// Build LLM request
	timeoutSecs := judgeTimeoutSeconds()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSecs)*time.Second)
	defer cancel()
	wrapped := judge.WrapAgentOutput(targetStr)
	userContent := wrapped
	if spec.Criteria != "" {
		userContent = fmt.Sprintf("Evaluation criteria: %s\n\n%s", spec.Criteria, wrapped)
	}

	runs, err := repeatRuns(spec)
	if err != nil {
		return failResult(assertion, start, err.Error())
	}
	probes, err := resolveBiasProbes(spec.BiasProbes)
	if err != nil {
		return failResult(assertion, start, err.Error())
	}
	if runs > 1 {
		return e.evaluateRepeated(ctx, assertion, rubric, model, userContent, spec, start, targetStr, rubricName, runs, probes)
	}

	return e.evaluateSinglePass(ctx, assertion, rubric, model, userContent, spec, start, targetStr, rubricName, probes)
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

	return &types.AssertionResult{
		AssertionID:     assertion.AssertionID,
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
	model, userContent string,
	spec judgeSpec,
	start time.Time,
	targetStr, rubricName string,
	probes []string,
) *types.AssertionResult {
	req := &llm.CompletionRequest{
		Model:        model,
		SystemPrompt: rubric.SystemPrompt,
		Messages:     []llm.Message{{Role: "user", Content: userContent}},
		Temperature:  0.0,
		MaxTokens:    256,
	}

	resp, err := e.provider.Complete(ctx, req)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("LLM call failed: %v", err))
	}

	scoreResult, err := judge.ParseScoreResult(resp.Content)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("parse judge response: %v", err))
	}

	durationMS := time.Since(start).Milliseconds()

	if e.cache != nil {
		contentHash := cache.JudgeContentHash(targetStr)
		if putErr := e.cache.Put(contentHash, rubricName, model, &cache.JudgeCacheEntry{
			Score:       scoreResult.Score,
			Explanation: scoreResult.Explanation,
		}); putErr != nil {
			slog.Error("judge cache write error", "err", putErr)
		}
	}

	probeResults, probeCost := runBiasProbes(ctx, e.provider, rubric, model, userContent, scoreResult.Score, probes)
	totalCost := resp.Cost + probeCost
	meta := &types.JudgeMetadata{
		Model:         model,
		RubricName:    rubricName,
		RubricVersion: rubric.Version,
		PromptHash:    judge.PromptHash(userContent),
		SampleScores:  []float64{scoreResult.Score},
		ScoreMean:     scoreResult.Score,
		BiasProbes:    probeResults,
	}
	durationMS = time.Since(start).Milliseconds()
	return e.buildResult(assertion, scoreResult.Score, scoreResult.Explanation, spec.Threshold, spec.Soft, durationMS, totalCost,
		judgeBuildArgs{target: spec.Target, model: model, rubric: rubricName, meta: meta})
}

// metaEvalResult holds one judge run's output.
type metaEvalResult struct {
	score       float64
	explanation string
	cost        float64
	err         error
}

// evaluateRepeated runs the judge `runs` times concurrently, takes the median
// score, and flags high variance in the explanation. Use to surface judge
// stability — sample stddev and per-sample scores are returned in
// JudgeMetadata so calibration tooling can compute agreement metrics.
func (e *JudgeEvaluator) evaluateRepeated(
	ctx context.Context,
	assertion *types.Assertion,
	rubric *judge.Rubric,
	model, userContent string,
	spec judgeSpec,
	start time.Time,
	targetStr, rubricName string,
	runs int,
	probes []string,
) *types.AssertionResult {
	results := make([]metaEvalResult, runs)
	var wg sync.WaitGroup

	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := &llm.CompletionRequest{
				Model:        model,
				SystemPrompt: rubric.SystemPrompt,
				Messages:     []llm.Message{{Role: "user", Content: userContent}},
				Temperature:  metaEvalTemperature,
				MaxTokens:    256,
			}

			resp, err := e.provider.Complete(ctx, req)
			if err != nil {
				results[idx] = metaEvalResult{err: err}
				return
			}

			sr, err := judge.ParseScoreResult(resp.Content)
			if err != nil {
				results[idx] = metaEvalResult{err: err}
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
	var firstErr error

	for i, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		scores = append(scores, r.score)
		explanations = append(explanations, fmt.Sprintf("Run %d: %s", i+1, r.explanation))
		totalCost += r.cost
	}

	// Need at least 1 successful run
	if len(scores) == 0 {
		return failResult(assertion, start, fmt.Sprintf("all %d judge runs failed: %v", runs, firstErr))
	}

	// Sort and take median
	sort.Float64s(scores)
	medianScore := scores[len(scores)/2]

	// Calculate variance (spread)
	spread := scores[len(scores)-1] - scores[0]
	var varianceNote string
	if spread > metaEvalVarianceThreshold {
		varianceNote = fmt.Sprintf(" [HIGH VARIANCE: spread=%.2f across %d runs]", spread, len(scores))
	}

	combinedExplanation := strings.Join(explanations, " | ") + " | Median selected." + varianceNote

	durationMS := time.Since(start).Milliseconds()

	// Cache the median result
	if e.cache != nil {
		contentHash := cache.JudgeContentHash(targetStr)
		if putErr := e.cache.Put(contentHash, rubricName, model, &cache.JudgeCacheEntry{
			Score:       medianScore,
			Explanation: combinedExplanation,
		}); putErr != nil {
			slog.Error("judge cache write error", "err", putErr)
		}
	}

	probeResults, probeCost := runBiasProbes(ctx, e.provider, rubric, model, userContent, medianScore, probes)
	totalCost += probeCost

	mean, stddev := scoreVarianceStats(scores)
	meta := &types.JudgeMetadata{
		Model:            model,
		RubricName:       rubricName,
		RubricVersion:    rubric.Version,
		PromptHash:       judge.PromptHash(userContent),
		SampleScores:     scores,
		ScoreMean:        mean,
		ScoreStddev:      stddev,
		HighVarianceFlag: spread > metaEvalVarianceThreshold,
		BiasProbes:       probeResults,
	}
	durationMS = time.Since(start).Milliseconds()
	return e.buildResult(assertion, medianScore, combinedExplanation, spec.Threshold, spec.Soft, durationMS, totalCost,
		judgeBuildArgs{target: spec.Target, model: model, rubric: rubricName, meta: meta})
}
