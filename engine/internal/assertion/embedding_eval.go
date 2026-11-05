package assertion

import (
	"context"
	"errors"
	"fmt"
	"github.com/segmentio/encoding/json"
	"log/slog"
	"time"

	"github.com/attest-framework/attest/engine/internal/assertion/embedding"
	"github.com/attest-framework/attest/engine/internal/cache"
	"github.com/attest-framework/attest/engine/internal/llm"
	"github.com/attest-framework/attest/engine/pkg/types"
)

// EmbeddingEvaluator implements Layer 5: semantic similarity assertions.
type EmbeddingEvaluator struct {
	embedder embedding.Embedder
	cache    *cache.EmbeddingCache
}

// NewEmbeddingEvaluator creates an evaluator using the given embedder and optional cache.
// cache may be nil to disable caching.
func NewEmbeddingEvaluator(embedder embedding.Embedder, c *cache.EmbeddingCache) *EmbeddingEvaluator {
	return &EmbeddingEvaluator{embedder: embedder, cache: c}
}

// embeddingSpec is the expected structure of the assertion spec JSON.
type embeddingSpec struct {
	Target    string  `json:"target"`
	Reference string  `json:"reference"`
	Threshold float64 `json:"threshold"`
	Soft      bool    `json:"soft"`
}

// Evaluate runs the embedding similarity assertion against the trace.
func (e *EmbeddingEvaluator) Evaluate(trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	return e.EvaluateContext(context.Background(), trace, assertion)
}

// EvaluateContext runs the assertion under ctx, which carries cancellation
// and the batch budget scope (WithBudgetScope).
func (e *EmbeddingEvaluator) EvaluateContext(ctx context.Context, trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	start := time.Now()

	var spec embeddingSpec
	if err := json.Unmarshal(assertion.Spec, &spec); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid embedding spec: %v", err))
	}
	if spec.Target == "" {
		return failResult(assertion, start, "embedding spec missing required field: target")
	}
	if spec.Reference == "" {
		return failResult(assertion, start, "embedding spec missing required field: reference")
	}
	if spec.Threshold <= 0 {
		spec.Threshold = 0.8 // sensible default
	}

	targetStr, err := ResolveTargetString(trace, spec.Target)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("target resolution failed: %v", err))
	}

	targetVec, err := e.getEmbedding(ctx, targetStr)
	if err != nil {
		return embeddingCallFailed(assertion, start, "target", err)
	}

	refVec, err := e.getEmbedding(ctx, spec.Reference)
	if err != nil {
		return embeddingCallFailed(assertion, start, "reference", err)
	}

	sim, err := embedding.CosineSimilarity(targetVec, refVec)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("cosine similarity: %v", err))
	}

	durationMS := time.Since(start).Milliseconds()
	score := sim
	if score < 0 {
		score = 0
	}

	expected := fmt.Sprintf("cosine_similarity(target, reference) >= %.4f", spec.Threshold)
	actual := fmt.Sprintf("cosine_similarity = %.4f against %q", sim, truncate(spec.Reference))

	if sim >= spec.Threshold {
		return &types.AssertionResult{
			AssertionID:   assertion.AssertionID,
			Status:        types.StatusPass,
			Score:         score,
			Explanation:   fmt.Sprintf("cosine similarity %.4f >= threshold %.4f", sim, spec.Threshold),
			DurationMS:    durationMS,
			RequestID:     assertion.RequestID,
			TraceNodePath: spec.Target,
			Expected:      expected,
			Actual:        actual,
		}
	}

	failStatus := types.StatusHardFail
	if spec.Soft {
		failStatus = types.StatusSoftFail
	}
	suggestion := "Inspect the agent response — semantic drift from reference. Lower threshold only if the reference is too narrow."
	if spec.Threshold > 0.9 {
		suggestion = "Threshold is high (>0.9). Consider whether paraphrasing should pass."
	}
	return &types.AssertionResult{
		AssertionID:     assertion.AssertionID,
		Status:          failStatus,
		Score:           score,
		Explanation:     fmt.Sprintf("cosine similarity %.4f < threshold %.4f", sim, spec.Threshold),
		DurationMS:      durationMS,
		RequestID:       assertion.RequestID,
		TraceNodePath:   spec.Target,
		Expected:        expected,
		Actual:          actual,
		SuggestedAction: suggestion,
	}
}

// getEmbedding retrieves an embedding vector, using cache if available.
func (e *EmbeddingEvaluator) getEmbedding(ctx context.Context, text string) ([]float32, error) {
	if e.cache != nil {
		h := cache.ContentHash(text)
		if cached, err := e.cache.Get(h, e.embedder.Model()); err == nil && cached != nil {
			return cached, nil
		}

		vec, err := e.embedder.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		// Best-effort cache write — do not fail on cache errors
		if putErr := e.cache.Put(h, e.embedder.Model(), vec); putErr != nil {
			slog.Error("embedding cache write error", "err", putErr)
		}
		return vec, nil
	}

	return e.embedder.Embed(ctx, text)
}

// embeddingCallFailed maps a failed embedding call on what ("target" or
// "reference") to not_evaluated: budget_exhausted when the budget refused
// it, otherwise provider_unavailable.
func embeddingCallFailed(assertion *types.Assertion, start time.Time, what string, err error) *types.AssertionResult {
	if errors.Is(err, llm.ErrBudgetExhausted) {
		return notEvaluatedResult(assertion, start, types.ReasonBudgetExhausted, fmt.Sprintf("embedding call on %s not sent: %v", what, err))
	}
	return notEvaluatedResult(assertion, start, types.ReasonProviderUnavailable, fmt.Sprintf("embedding provider failed on %s: %v", what, err))
}
