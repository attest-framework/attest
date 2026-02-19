package assertion

import (
	"fmt"

	"github.com/attest-ai/attest/engine/internal/assertion/embedding"
	"github.com/attest-ai/attest/engine/internal/assertion/judge"
	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// Evaluator is the interface for assertion evaluators.
type Evaluator interface {
	Evaluate(trace *types.Trace, assertion *types.Assertion) *types.AssertionResult
}

// Registry maps assertion type strings to Evaluator implementations.
type Registry struct {
	evaluators map[string]Evaluator
}

// registryConfig holds optional Layer 5/6 configuration.
type registryConfig struct {
	embedder      embedding.Embedder
	embeddingCache *cache.EmbeddingCache
	judgeProvider  llm.Provider
	rubrics        *judge.RubricRegistry
	judgeCache     *cache.JudgeCache
}

// RegistryOption configures optional evaluators on a Registry.
type RegistryOption func(*registryConfig)

// WithEmbedding enables Layer 5 (embedding similarity) evaluation.
func WithEmbedding(embedder embedding.Embedder, c *cache.EmbeddingCache) RegistryOption {
	return func(cfg *registryConfig) {
		cfg.embedder = embedder
		cfg.embeddingCache = c
	}
}

// WithJudge enables Layer 6 (LLM judge) evaluation.
func WithJudge(provider llm.Provider, rubrics *judge.RubricRegistry, c *cache.JudgeCache) RegistryOption {
	return func(cfg *registryConfig) {
		cfg.judgeProvider = provider
		cfg.rubrics = rubrics
		cfg.judgeCache = c
	}
}

// NewRegistry creates a registry with built-in evaluators registered.
// Layers 1-4 are always registered. Layers 5-6 are registered when the
// corresponding RegistryOption is provided.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{
		evaluators: make(map[string]Evaluator),
	}
	r.Register(types.TypeSchema, &SchemaEvaluator{})
	r.Register(types.TypeConstraint, &ConstraintEvaluator{})
	r.Register(types.TypeTrace, &TraceEvaluator{})
	r.Register(types.TypeTraceTree, &TraceTreeEvaluator{})
	r.Register(types.TypeContent, &ContentEvaluator{})

	var cfg registryConfig
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.embedder != nil {
		r.Register(types.TypeEmbedding, NewEmbeddingEvaluator(cfg.embedder, cfg.embeddingCache))
	}
	if cfg.judgeProvider != nil && cfg.rubrics != nil {
		r.Register(types.TypeLLMJudge, NewJudgeEvaluator(cfg.judgeProvider, cfg.rubrics, cfg.judgeCache))
	}

	return r
}

// HasEvaluator returns true if an evaluator is registered for the given type.
func (r *Registry) HasEvaluator(assertionType string) bool {
	_, ok := r.evaluators[assertionType]
	return ok
}

// Register adds an evaluator for an assertion type.
func (r *Registry) Register(assertionType string, eval Evaluator) {
	r.evaluators[assertionType] = eval
}

// Get returns the evaluator for an assertion type, or error if not found.
func (r *Registry) Get(assertionType string) (Evaluator, error) {
	eval, ok := r.evaluators[assertionType]
	if !ok {
		return nil, fmt.Errorf("unknown assertion type: %s", assertionType)
	}
	return eval, nil
}
