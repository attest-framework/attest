package assertion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
type judgeSpec struct {
	Target    string  `json:"target"`
	Criteria  string  `json:"criteria"`
	Rubric    string  `json:"rubric"`
	Threshold float64 `json:"threshold"`
	Soft      bool    `json:"soft"`
	Model     string  `json:"model"`
	MetaEval  bool    `json:"meta_eval"`
}

const metaEvalRuns = 3
const metaEvalTemperature = 0.3
const metaEvalVarianceThreshold = 0.2

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
				Model:        model,
				RubricName:   rubricName,
				PromptHash:   promptHash(targetStr),
				SampleScores: []float64{cached.Score},
				ScoreMean:    cached.Score,
			}
			return e.buildResult(assertion, cached.Score, cached.Explanation, spec.Threshold, spec.Soft, durationMS, 0,
				judgeBuildArgs{target: spec.Target, model: model, rubric: rubricName, prompt: targetStr, meta: meta})
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

	if metaEvalEnabled(spec) {
		return e.evaluateWithMetaEval(ctx, assertion, rubric, model, userContent, spec, start, targetStr, rubricName)
	}

	return e.evaluateSinglePass(ctx, assertion, rubric, model, userContent, spec, start, targetStr, rubricName)
}

// judgeBuildArgs bundles the optional diagnostic inputs to buildResult so
// the function signature does not balloon past four positional parameters.
type judgeBuildArgs struct {
	target string
	model  string
	rubric string
	prompt string
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

// promptHash hashes the user content the judge model received so report
// readers and calibration tools can correlate results across runs without
// storing the prompt itself.
func promptHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:8])
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

// metaEvalEnabled returns true if meta-evaluation is requested via spec or env var.
func metaEvalEnabled(spec judgeSpec) bool {
	if spec.MetaEval {
		return true
	}
	return os.Getenv("ATTEST_JUDGE_META_EVAL") == "true"
}

// evaluateSinglePass runs the judge once (default behavior).
func (e *JudgeEvaluator) evaluateSinglePass(
	ctx context.Context,
	assertion *types.Assertion,
	rubric *judge.Rubric,
	model, userContent string,
	spec judgeSpec,
	start time.Time,
	targetStr, rubricName string,
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

	meta := &types.JudgeMetadata{
		Model:        model,
		RubricName:   rubricName,
		PromptHash:   promptHash(userContent),
		SampleScores: []float64{scoreResult.Score},
		ScoreMean:    scoreResult.Score,
	}
	return e.buildResult(assertion, scoreResult.Score, scoreResult.Explanation, spec.Threshold, spec.Soft, durationMS, resp.Cost,
		judgeBuildArgs{target: spec.Target, model: model, rubric: rubricName, prompt: userContent, meta: meta})
}

// metaEvalResult holds one judge run's output.
type metaEvalResult struct {
	score       float64
	explanation string
	cost        float64
	err         error
}

// evaluateWithMetaEval runs the judge 3x concurrently, takes the median score,
// and flags high variance in the explanation.
func (e *JudgeEvaluator) evaluateWithMetaEval(
	ctx context.Context,
	assertion *types.Assertion,
	rubric *judge.Rubric,
	model, userContent string,
	spec judgeSpec,
	start time.Time,
	targetStr, rubricName string,
) *types.AssertionResult {
	results := make([]metaEvalResult, metaEvalRuns)
	var wg sync.WaitGroup

	for i := 0; i < metaEvalRuns; i++ {
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
		return failResult(assertion, start, fmt.Sprintf("all %d meta-eval runs failed: %v", metaEvalRuns, firstErr))
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

	mean, stddev := scoreVarianceStats(scores)
	meta := &types.JudgeMetadata{
		Model:            model,
		RubricName:       rubricName,
		PromptHash:       promptHash(userContent),
		SampleScores:     scores,
		ScoreMean:        mean,
		ScoreStddev:      stddev,
		HighVarianceFlag: spread > metaEvalVarianceThreshold,
	}
	return e.buildResult(assertion, medianScore, combinedExplanation, spec.Threshold, spec.Soft, durationMS, totalCost,
		judgeBuildArgs{target: spec.Target, model: model, rubric: rubricName, prompt: userContent, meta: meta})
}
