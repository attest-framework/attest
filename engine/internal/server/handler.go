package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/internal/assertion/embedding"
	"github.com/attest-ai/attest/engine/internal/assertion/judge"
	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/internal/simulation"
	"github.com/attest-ai/attest/engine/internal/trace"
	"github.com/attest-ai/attest/engine/pkg/types"
)

const (
	engineVersion   = "0.3.0"
	protocolVersion = 1
)

// RegisterBuiltinHandlers registers the built-in JSON-RPC handlers on s.
// It reads ATTEST_* env vars to configure Layer 5/6 providers and caches.
func RegisterBuiltinHandlers(s *Server) {
	opts, caps, judgeProvider := buildRegistryOptions(s.logger)
	registry := assertion.NewRegistry(opts...)
	pipeline := assertion.NewPipeline(registry)

	s.RegisterHandler("initialize", handleInitialize(caps))
	s.RegisterHandler("shutdown", handleShutdown)
	s.RegisterHandler("evaluate_batch", handleEvaluateBatch(pipeline))
	s.RegisterHandler("submit_plugin_result", handleSubmitPluginResult())
	s.RegisterHandler("validate_trace_tree", handleValidateTraceTree())
	if judgeProvider != nil {
		s.RegisterHandler("generate_user_message", handleGenerateUserMessage(judgeProvider))
	}
}

// buildRegistryOptions reads env vars and constructs RegistryOption values
// for Layer 5 (embedding) and Layer 6 (judge) evaluators. Returns the
// options, the list of supported capabilities, and the judge provider (may be nil).
func buildRegistryOptions(logger *slog.Logger) ([]assertion.RegistryOption, []string, llm.Provider) {
	caps := []string{"layers_1_4", "trace_tree"}
	var opts []assertion.RegistryOption

	// ── Layer 5: Embedding ──
	openAIKey := os.Getenv("ATTEST_OPENAI_API_KEY")
	embeddingProvider := os.Getenv("ATTEST_EMBEDDING_PROVIDER") // "openai" or "auto" (default)
	if embeddingProvider == "" {
		embeddingProvider = "auto"
	}

	var embedder embedding.Embedder
	var embProviderName string

	if openAIKey != "" && (embeddingProvider == "auto" || embeddingProvider == "openai") {
		e, err := embedding.NewOpenAIEmbedder(embedding.EmbedderConfig{
			APIKey: openAIKey,
		})
		if err != nil {
			logger.Warn("failed to create OpenAI embedder", "err", err)
		} else {
			embedder = e
			embProviderName = "openai"
		}
	}

	// ONNX fallback: explicit "onnx" provider or auto-detect when no OpenAI key
	if embedder == nil && (embeddingProvider == "onnx" || (embeddingProvider == "auto" && openAIKey == "")) {
		if embedding.ONNXAvailable {
			modelDir := os.Getenv("ATTEST_ONNX_MODEL_DIR")
			e, err := embedding.NewONNXEmbedder(embedding.EmbedderConfig{ModelDir: modelDir})
			if err != nil {
				logger.Warn("failed to create ONNX embedder", "err", err)
			} else {
				embedder = e
				embProviderName = "onnx"
			}
		} else if embeddingProvider == "onnx" {
			logger.Warn("ONNX embedding requested but not compiled in — rebuild with -tags onnx")
		}
	}

	if embedder != nil {
		var embCache *cache.EmbeddingCache
		cacheDir := cacheDirectory()
		maxMB := envInt("ATTEST_EMBEDDING_CACHE_MAX_MB", 500)
		dbPath := filepath.Join(cacheDir, "attest.db")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			logger.Warn("failed to create cache dir", "dir", cacheDir, "err", err)
		} else {
			c, err := cache.NewEmbeddingCache(dbPath, maxMB)
			if err != nil {
				logger.Warn("failed to create embedding cache", "err", err)
			} else {
				embCache = c
			}
		}
		opts = append(opts, assertion.WithEmbedding(embedder, embCache))
		caps = append(caps, "embedding")
		logger.Info("layer 5 (embedding) enabled", "provider", embProviderName)
	}

	// ── Layer 6: LLM Judge ──
	judgeProvider, providerName := buildJudgeProvider(logger)
	if judgeProvider != nil {
		rubrics := judge.NewRubricRegistry()

		var jCache *cache.JudgeCache
		cacheDir := cacheDirectory()
		dbPath := filepath.Join(cacheDir, "attest.db")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			logger.Warn("failed to create cache dir", "dir", cacheDir, "err", err)
		} else {
			c, err := cache.NewJudgeCache(dbPath, 100)
			if err != nil {
				logger.Warn("failed to create judge cache", "err", err)
			} else {
				jCache = c
			}
		}
		opts = append(opts, assertion.WithJudge(judgeProvider, rubrics, jCache))
		caps = append(caps, "llm_judge", "simulation")
		logger.Info("layer 6 (judge) enabled", "provider", providerName)
	}

	if embedder != nil || judgeProvider != nil {
		caps = append(caps, "layers_5_6")
	}

	return opts, caps, judgeProvider
}

// buildJudgeProvider selects and constructs an LLM provider for judging.
// Reads ATTEST_JUDGE_PROVIDER and corresponding API keys.
func buildJudgeProvider(logger *slog.Logger) (llm.Provider, string) {
	preferred := os.Getenv("ATTEST_JUDGE_PROVIDER")
	model := os.Getenv("ATTEST_JUDGE_MODEL")

	// Try preferred provider first, then fall through in priority order
	providers := []string{"openai", "anthropic", "gemini", "ollama"}
	if preferred != "" {
		providers = []string{preferred}
	}

	for _, name := range providers {
		switch name {
		case "openai":
			key := os.Getenv("ATTEST_OPENAI_API_KEY")
			if key == "" {
				continue
			}
			p, err := llm.NewOpenAIProvider(key, model, "")
			if err != nil {
				logger.Warn("failed to create OpenAI judge provider", "err", err)
				continue
			}
			return p, "openai"

		case "anthropic":
			// Anthropic provider not implemented yet in llm package — skip
			continue

		case "gemini":
			// Gemini provider not implemented yet in llm package — skip
			continue

		case "ollama":
			// Ollama provider not implemented yet in llm package — skip
			continue
		}
	}

	return nil, ""
}

// cacheDirectory returns the cache directory from env or default.
func cacheDirectory() string {
	if dir := os.Getenv("ATTEST_CACHE_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".attest", "cache")
}

// envInt reads an int from an env var with a fallback default.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func handleInitialize(caps []string) Handler {
	return func(session *Session, params json.RawMessage) (any, *types.RPCError) {
		if session.State() != StateUninitialized {
			return nil, types.NewRPCError(
				types.ErrSessionError,
				"initialize called on already-initialized session",
				types.ErrTypeSessionError,
				false,
				"initialize may only be called once per session",
			)
		}

		var p types.InitializeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, types.NewRPCError(
				types.ErrSessionError,
				"invalid initialize params",
				types.ErrTypeSessionError,
				false,
				err.Error(),
			)
		}

		if p.ProtocolVersion != protocolVersion {
			return nil, types.NewRPCError(
				types.ErrSessionError,
				fmt.Sprintf("protocol version %d not supported; engine supports version %d", p.ProtocolVersion, protocolVersion),
				types.ErrTypeSessionError,
				false,
				"Upgrade the engine binary or downgrade the SDK protocol_version",
			)
		}

		// Compute missing capabilities.
		supported := make(map[string]bool, len(caps))
		for _, c := range caps {
			supported[c] = true
		}

		var missing []string
		for _, req := range p.RequiredCapabilities {
			if !supported[req] {
				missing = append(missing, req)
			}
		}

		compatible := len(missing) == 0
		if missing == nil {
			missing = []string{}
		}

		session.SetState(StateInitialized)

		return &types.InitializeResult{
			EngineVersion:         engineVersion,
			ProtocolVersion:       protocolVersion,
			Capabilities:          caps,
			Missing:               missing,
			Compatible:            compatible,
			Encoding:              "json",
			MaxConcurrentRequests: 64,
			MaxTraceSizeBytes:     10 * 1024 * 1024,
			MaxStepsPerTrace:      10000,
		}, nil
	}
}

func handleShutdown(session *Session, _ json.RawMessage) (any, *types.RPCError) {
	if session.State() != StateInitialized {
		return nil, types.NewRPCError(
			types.ErrSessionError,
			"shutdown called on uninitialized or already-shutting-down session",
			types.ErrTypeSessionError,
			false,
			"call initialize before shutdown",
		)
	}

	session.SetState(StateShuttingDown)

	// Increment completed session count before reading stats.
	session.mu.Lock()
	session.sessionsCompleted++
	completed := session.sessionsCompleted
	evaluated := session.assertionsEvaluated
	session.mu.Unlock()

	return &types.ShutdownResult{
		SessionsCompleted:   int(completed),
		AssertionsEvaluated: int(evaluated),
	}, nil
}

func handleEvaluateBatch(pipeline *assertion.Pipeline) Handler {
	return func(session *Session, params json.RawMessage) (any, *types.RPCError) {
		if session.State() != StateInitialized {
			return nil, types.NewRPCError(
				types.ErrSessionError,
				"evaluate_batch called before initialize",
				types.ErrTypeSessionError,
				false,
				"call initialize first to establish a session before sending evaluate_batch requests",
			)
		}

		var p types.EvaluateBatchParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, types.NewRPCError(
				types.ErrInvalidTrace,
				fmt.Sprintf("invalid evaluate_batch params: %v", err),
				types.ErrTypeInvalidTrace,
				false,
				"Check the request format matches the protocol spec.",
			)
		}

		trace.Normalize(&p.Trace)
		if rpcErr := trace.Validate(&p.Trace); rpcErr != nil {
			return nil, rpcErr
		}

		result, err := pipeline.EvaluateBatch(&p.Trace, p.Assertions)
		if err != nil {
			return nil, types.NewRPCError(
				types.ErrEngineError,
				fmt.Sprintf("evaluation failed: %v", err),
				types.ErrTypeEngineError,
				false,
				"Internal engine error during evaluation.",
			)
		}

		session.IncrementAssertions(len(result.Results))

		return &types.EvaluateBatchResult{
			Results:         result.Results,
			TotalCost:       result.TotalCost,
			TotalDurationMS: result.TotalDurationMS,
		}, nil
	}
}

func handleSubmitPluginResult() Handler {
	return func(session *Session, params json.RawMessage) (any, *types.RPCError) {
		if session.State() != StateInitialized {
			return nil, types.NewRPCError(
				types.ErrSessionError,
				"submit_plugin_result called before initialize",
				types.ErrTypeSessionError,
				false,
				"call initialize first to establish a session",
			)
		}

		var p types.SubmitPluginResultParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, types.NewRPCError(
				types.ErrAssertionError,
				"invalid submit_plugin_result params",
				types.ErrTypeAssertionError,
				false,
				err.Error(),
			)
		}

		session.IncrementAssertions(1)

		return &types.SubmitPluginResultResponse{Accepted: true}, nil
	}
}

func handleValidateTraceTree() Handler {
	return func(session *Session, params json.RawMessage) (any, *types.RPCError) {
		if session.State() != StateInitialized {
			return nil, types.NewRPCError(
				types.ErrSessionError,
				"validate_trace_tree called before initialize",
				types.ErrTypeSessionError,
				false,
				"call initialize first to establish a session",
			)
		}

		var p types.ValidateTraceTreeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, types.NewRPCError(
				types.ErrInvalidTrace,
				"invalid validate_trace_tree params",
				types.ErrTypeInvalidTrace,
				false,
				err.Error(),
			)
		}

		result := &types.ValidateTraceTreeResult{}

		if err := trace.ValidateTraceTree(&p.Trace); err != nil {
			result.Valid = false
			result.Errors = []string{err.Error()}
		} else {
			result.Valid = true
		}

		result.Depth = trace.TreeDepth(&p.Trace)
		agentIDs := trace.AgentIDs(&p.Trace)
		result.AgentIDs = agentIDs
		result.AgentCount = len(agentIDs)

		totalTokens, totalCostUSD, totalLatencyMS, _ := trace.AggregateMetadata(&p.Trace)
		result.AggregateTokens = totalTokens
		result.AggregateCostUSD = totalCostUSD
		result.AggregateLatencyMS = totalLatencyMS

		return result, nil
	}
}

func handleGenerateUserMessage(provider llm.Provider) Handler {
	return func(session *Session, params json.RawMessage) (any, *types.RPCError) {
		if session.State() != StateInitialized {
			return nil, types.NewRPCError(
				types.ErrSessionError,
				"generate_user_message called before initialize",
				types.ErrTypeSessionError,
				false,
				"call initialize first to establish a session",
			)
		}

		var p types.GenerateUserMessageParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, types.NewRPCError(
				types.ErrAssertionError,
				"invalid generate_user_message params",
				types.ErrTypeAssertionError,
				false,
				err.Error(),
			)
		}

		persona := simulation.Persona{
			Name:         p.Persona.Name,
			SystemPrompt: p.Persona.SystemPrompt,
			Style:        p.Persona.Style,
			Temperature:  p.Persona.Temperature,
			MaxTokens:    p.Persona.MaxTokens,
		}

		var prov llm.Provider = provider
		if p.FaultConfig != nil {
			fc := simulation.FaultConfig{
				ErrorRate:         p.FaultConfig.ErrorRate,
				LatencyJitter:     time.Duration(p.FaultConfig.LatencyJitterMS) * time.Millisecond,
				ContentCorruption: p.FaultConfig.ContentCorruption,
				TimeoutAfter:      time.Duration(p.FaultConfig.TimeoutAfterMS) * time.Millisecond,
			}
			prov = simulation.NewFaultInjector(prov, fc)
		}

		user := simulation.NewSimulatedUser(persona, prov)

		messages := make([]llm.Message, 0, len(p.ConversationHistory))
		for _, m := range p.ConversationHistory {
			messages = append(messages, llm.Message{Role: m.Role, Content: m.Content})
		}

		msg, err := user.GenerateMessage(context.Background(), messages)
		if err != nil {
			return nil, types.NewRPCError(
				types.ErrEngineError,
				fmt.Sprintf("generate_user_message failed: %v", err),
				types.ErrTypeEngineError,
				true,
				"check LLM provider availability and retry",
			)
		}

		return &types.GenerateUserMessageResult{Message: msg}, nil
	}
}
