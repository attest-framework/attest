package assertion

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/attest-ai/attest/engine/internal/assertion/embedding"
	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/pkg/types"
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
	Target     string  `json:"target"`
	Reference  string  `json:"reference"`
	Threshold  float64 `json:"threshold"`
	Soft       bool    `json:"soft"`
}

// Evaluate runs the embedding similarity assertion against the trace.
func (e *EmbeddingEvaluator) Evaluate(trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
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

	ctx := context.Background()

	targetVec, err := e.getEmbedding(ctx, targetStr)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("embed target: %v", err))
	}

	refVec, err := e.getEmbedding(ctx, spec.Reference)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("embed reference: %v", err))
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

	if sim >= spec.Threshold {
		return &types.AssertionResult{
			AssertionID: assertion.AssertionID,
			Status:      types.StatusPass,
			Score:       score,
			Explanation: fmt.Sprintf("cosine similarity %.4f >= threshold %.4f", sim, spec.Threshold),
			DurationMS:  durationMS,
			RequestID:   assertion.RequestID,
		}
	}

	failStatus := types.StatusHardFail
	if spec.Soft {
		failStatus = types.StatusSoftFail
	}
	return &types.AssertionResult{
		AssertionID: assertion.AssertionID,
		Status:      failStatus,
		Score:       score,
		Explanation: fmt.Sprintf("cosine similarity %.4f < threshold %.4f", sim, spec.Threshold),
		DurationMS:  durationMS,
		RequestID:   assertion.RequestID,
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
