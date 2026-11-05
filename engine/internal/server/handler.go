package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/segmentio/encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/internal/assertion/judge"
	"github.com/attest-ai/attest/engine/internal/buildinfo"
	"github.com/attest-ai/attest/engine/internal/cache"
	"github.com/attest-ai/attest/engine/internal/contractlog"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/internal/simulation"
	"github.com/attest-ai/attest/engine/internal/trace"
	"github.com/attest-ai/attest/engine/pkg/types"
	_ "modernc.org/sqlite"
)

const (
	protocolVersion      = 1
	minProtocolVersion   = 1
	MaxAssertionIDLength = 256
)

// ProtocolVersion is the newest JSON-RPC protocol version the engine speaks.
const ProtocolVersion = protocolVersion

// RegisterBuiltinHandlers registers the built-in JSON-RPC handlers on s.
// It reads ATTEST_* env vars to configure Layer 5/6 providers and caches.
// Returns an error when any explicitly-set env var has an invalid value, so the
// operator sees broken config at startup rather than after an evaluation runs
// with silently-defaulted values.
func RegisterBuiltinHandlers(s *Server) error {
	if err := validateEngineEnv(); err != nil {
		return err
	}
	opts, caps, provs, historyStore, err := buildRegistryOptions(s.logger)
	if err != nil {
		return err
	}
	registry := assertion.NewRegistry(opts...)

	var pipeline *assertion.Pipeline
	if historyStore != nil {
		pipeline = assertion.NewPipelineWithHistory(registry, historyStore)
	} else {
		pipeline = assertion.NewPipeline(registry)
	}
	pipeline.SetSimulation(assertion.SimulationModeFromEnv())

	concurrency, err := envInt("ATTEST_EVAL_CONCURRENCY", 0)
	if err != nil {
		return err
	}
	if concurrency > 0 {
		pipeline.SetEvalConcurrency(concurrency)
		s.logger.Info("eval concurrency set", "limit", concurrency)
	}

	// Wire BudgetTracker from ATTEST_MAX_SOFT_FAILURES (nil when unset).
	budget, err := buildBudgetTracker(s.logger)
	if err != nil {
		return err
	}

	recorder, err := contractlog.NewFromEnv()
	if err != nil {
		return err
	}
	if recorder != nil {
		s.logger.Info("recording assertion specs", "file", recorder.Path())
	}

	s.RegisterHandler("initialize", handleInitialize(caps))
	s.RegisterHandler("shutdown", handleShutdown)
	s.RegisterHandler("evaluate_batch", handleEvaluateBatch(pipeline, historyStore, batchBudgets{softFail: budget, ledger: provs.ledger}, recorder))
	s.RegisterHandler("submit_plugin_result", handleSubmitPluginResult(historyStore))
	s.RegisterHandler("validate_trace_tree", handleValidateTraceTree())
	s.RegisterHandler("query_drift", handleQueryDrift(historyStore))
	if provs.judge != nil {
		s.RegisterHandler("generate_user_message", handleGenerateUserMessage(provs.judge))
	}
	return nil
}

// buildRegistryOptions reads env vars and constructs RegistryOption values
// for Layer 5 (embedding) and Layer 6 (judge) evaluators. Returns the
// options, the list of supported capabilities, the judge provider (may be nil),
// and the HistoryStore (may be nil on failure). Returns an error when any
// explicitly-set env var has an invalid value.
func buildRegistryOptions(logger *slog.Logger) ([]assertion.RegistryOption, []string, providers, *cache.HistoryStore, error) {
	provs, err := buildProviders(logger)
	if err != nil {
		return nil, nil, providers{}, nil, err
	}
	simulation := assertion.SimulationModeFromEnv()
	caps := provs.capabilities(simulation)
	var opts []assertion.RegistryOption

	// ── Layer 5: Embedding ──
	if provs.embedder != nil {
		var embCache *cache.EmbeddingCache
		cacheDir := cacheDirectory()
		maxMB, err := envInt("ATTEST_EMBEDDING_CACHE_MAX_MB", 500)
		if err != nil {
			return nil, nil, providers{}, nil, err
		}
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
		opts = append(opts, assertion.WithEmbedding(provs.embedder, embCache))
		logger.Info("layer 5 (embedding) enabled", "provider", provs.embeddingProvider)
	}

	// ── Layer 6: LLM Judge ──
	if provs.judge != nil {
		rubrics := judge.NewRubricRegistry()

		var jCache *cache.JudgeCache
		cacheDir := cacheDirectory()
		dbPath := filepath.Join(cacheDir, "attest.db")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			logger.Warn("failed to create cache dir", "dir", cacheDir, "err", err)
		} else {
			judgeCacheMaxMB, envErr := envInt("ATTEST_JUDGE_CACHE_MAX_MB", 100)
			if envErr != nil {
				return nil, nil, providers{}, nil, envErr
			}
			if judgeCacheMaxMB < 10 {
				judgeCacheMaxMB = 10
			} else if judgeCacheMaxMB > 10000 {
				judgeCacheMaxMB = 10000
			}
			c, err := cache.NewJudgeCache(dbPath, judgeCacheMaxMB, provs.judgeConfig.cacheEndpoint())
			if err != nil {
				logger.Warn("failed to create judge cache", "err", err)
			} else {
				jCache = c
			}
		}
		opts = append(opts, assertion.WithJudge(provs.judge, rubrics, jCache))
		logger.Info("layer 6 (judge) enabled",
			"provider", provs.judgeConfig.provider, "base_url", provs.judgeConfig.baseURL)
	}

	if simulation {
		logger.Info("simulation mode: layers 5-6 disabled")
	}

	// ── History Store ──
	var historyStore *cache.HistoryStore
	{
		cacheDir := cacheDirectory()
		dbPath := filepath.Join(cacheDir, "attest.db")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			logger.Warn("failed to create cache dir for history store", "dir", cacheDir, "err", err)
		} else {
			db, err := openHistoryDB(dbPath)
			if err != nil {
				logger.Warn("failed to open history db", "err", err)
			} else {
				hs, err := cache.NewHistoryStore(db)
				if err != nil {
					logger.Warn("failed to create history store", "err", err)
					db.Close()
				} else {
					// Configure retention from env vars.
					maxRows, envErr := envInt("ATTEST_HISTORY_MAX_ROWS", 0)
					if envErr != nil {
						db.Close()
						return nil, nil, providers{}, nil, envErr
					}
					maxDays, envErr := envInt("ATTEST_HISTORY_MAX_AGE_DAYS", 0)
					if envErr != nil {
						db.Close()
						return nil, nil, providers{}, nil, envErr
					}
					if maxRows > 0 || maxDays > 0 {
						if maxRows <= 0 {
							maxRows = 10000
						}
						if maxDays <= 0 {
							maxDays = 30
						}
						hs.SetPruneConfig(maxRows, maxDays)
					}
					historyStore = hs
					logger.Info("history store enabled", "db", dbPath)
				}
			}
		}
	}

	return opts, caps, provs, historyStore, nil
}

// openHistoryDB opens the SQLite database at dbPath for the history store.
func openHistoryDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	return db, nil
}

// cacheDirectory returns the cache directory from env or default.
func cacheDirectory() string {
	if dir := os.Getenv("ATTEST_CACHE_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".attest", "cache")
}

// envInt reads an int from an env var. Returns fallback when the var is unset
// or empty. Returns an error when the var is set but cannot be parsed as an
// integer — callers must surface this to the operator instead of silently
// substituting a default for broken config.
func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("env var %s=%q is not a valid integer: %w", key, v, err)
	}
	return n, nil
}

// validateEngineEnv checks every integer setting the server reads at startup,
// including those consulted only when a provider is configured, so the server
// and doctor reject the same broken configuration.
func validateEngineEnv() error {
	for _, key := range []string{
		"ATTEST_EVAL_CONCURRENCY", "ATTEST_EMBEDDING_CACHE_MAX_MB", "ATTEST_JUDGE_CACHE_MAX_MB",
		"ATTEST_HISTORY_MAX_ROWS", "ATTEST_HISTORY_MAX_AGE_DAYS",
	} {
		if _, err := envInt(key, 0); err != nil {
			return err
		}
	}
	_, _, err := maxSoftFailures()
	return err
}

// maxSoftFailures reads ATTEST_MAX_SOFT_FAILURES, the maximum number of soft
// failures allowed in one engine session; the count accumulates across
// batches. set is false when the variable is unset. A value of 0 means no
// soft failures are permitted; negative or non-integer values are errors.
func maxSoftFailures() (limit int, set bool, err error) {
	v := os.Getenv("ATTEST_MAX_SOFT_FAILURES")
	if v == "" {
		return 0, false, nil
	}
	limit, err = strconv.Atoi(v)
	if err != nil {
		return 0, false, fmt.Errorf("env var ATTEST_MAX_SOFT_FAILURES=%q is not a valid integer: %w", v, err)
	}
	if limit < 0 {
		return 0, false, fmt.Errorf("env var ATTEST_MAX_SOFT_FAILURES=%d must be >= 0", limit)
	}
	return limit, true, nil
}

// buildBudgetTracker constructs a BudgetTracker from ATTEST_MAX_SOFT_FAILURES.
// Returns nil when the variable is unset and an error when it is invalid.
func buildBudgetTracker(logger *slog.Logger) (*assertion.BudgetTracker, error) {
	limit, set, err := maxSoftFailures()
	if err != nil || !set {
		return nil, err
	}
	logger.Info("soft-failure limit enabled", "max_soft_failures", limit)
	return assertion.NewBudgetTracker(limit), nil
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

		if p.ProtocolVersion > protocolVersion {
			return nil, types.NewRPCError(
				types.ErrSessionError,
				fmt.Sprintf("protocol version %d not supported; engine supports versions %d–%d", p.ProtocolVersion, minProtocolVersion, protocolVersion),
				types.ErrTypeSessionError,
				false,
				"Upgrade the engine binary to support this SDK's protocol version",
			)
		}
		if p.ProtocolVersion < minProtocolVersion {
			return nil, types.NewRPCError(
				types.ErrSessionError,
				fmt.Sprintf("protocol version %d is too old; engine supports versions %d–%d", p.ProtocolVersion, minProtocolVersion, protocolVersion),
				types.ErrTypeSessionError,
				false,
				"Upgrade the SDK to a newer protocol version",
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
			EngineVersion:         buildinfo.Version,
			ProtocolVersion:       protocolVersion,
			Capabilities:          caps,
			Missing:               missing,
			Compatible:            compatible,
			Encoding:              "json",
			MaxConcurrentRequests: 1,
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

// batchBudgets holds the session budgets applied to each evaluate_batch:
// the soft-fail counter (ATTEST_MAX_SOFT_FAILURES, may be nil) and the
// model-call ledger.
type batchBudgets struct {
	softFail *assertion.BudgetTracker
	ledger   *assertion.Ledger
}

func handleEvaluateBatch(pipeline *assertion.Pipeline, historyStore *cache.HistoryStore, budgets batchBudgets, recorder *contractlog.Recorder) Handler {
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

		// E6: Validate assertion ID lengths before processing.
		for _, a := range p.Assertions {
			if len(a.AssertionID) > MaxAssertionIDLength {
				return nil, types.NewRPCError(
					types.ErrAssertionError,
					fmt.Sprintf("assertion_id exceeds maximum length: %d > %d", len(a.AssertionID), MaxAssertionIDLength),
					types.ErrTypeAssertionError,
					false,
					fmt.Sprintf("assertion_id must be at most %d characters", MaxAssertionIDLength),
				)
			}
		}

		trace.Normalize(&p.Trace)
		// Compute trace size once to avoid re-serialization in Validate.
		traceBytes, marshalErr := json.Marshal(&p.Trace)
		if marshalErr != nil {
			return nil, types.NewRPCError(
				types.ErrInvalidTrace,
				"trace could not be serialized for size check",
				types.ErrTypeInvalidTrace,
				false,
				"Ensure all trace fields contain valid JSON-serializable values.",
			)
		}
		if rpcErr := trace.Validate(&p.Trace, len(traceBytes)); rpcErr != nil {
			return nil, rpcErr
		}

		type assertionMeta struct {
			assertionType string
			dynamic       bool
		}
		assertionMap := make(map[string]assertionMeta, len(p.Assertions))
		for _, a := range p.Assertions {
			meta := assertionMeta{assertionType: a.Type}
			var spec struct {
				Threshold string `json:"threshold"`
			}
			if a.Spec != nil {
				if err := json.Unmarshal(a.Spec, &spec); err == nil && spec.Threshold == "dynamic" {
					meta.dynamic = true
				}
			}
			assertionMap[a.AssertionID] = meta
		}

		if rpcErr := validateBatchBudget(p.Budget); rpcErr != nil {
			return nil, rpcErr
		}
		if recorder != nil {
			// Only validated batches, and only assertion types the engine knows.
			var known []types.Assertion
			for _, a := range p.Assertions {
				if types.LayerForType(a.Type) != 0 {
					known = append(known, a)
				}
			}
			if err := recorder.Record(p.Context, known); err != nil {
				return nil, types.NewRPCError(
					types.ErrEngineError,
					fmt.Sprintf("%s: %v", contractlog.EnvDir, err),
					types.ErrTypeEngineError,
					false,
					"Fix the directory named by ATTEST_CONTRACTS_DIR or unset it.",
				)
			}
		}

		scope := budgets.ledger.NewScope(p.Budget)
		ctx := assertion.WithBudgetScope(context.Background(), scope)
		result, err := pipeline.EvaluateBatchWithBudget(ctx, &p.Trace, p.Assertions, budgets.softFail)
		if err != nil {
			return nil, types.NewRPCError(
				types.ErrEngineError,
				fmt.Sprintf("evaluation failed: %v", err),
				types.ErrTypeEngineError,
				false,
				"Internal engine error during evaluation.",
			)
		}

		if historyStore != nil {
			for i := range result.Results {
				ar := &result.Results[i]
				meta := assertionMap[ar.AssertionID]
				// E3: Log history store record errors instead of silently discarding.
				if recErr := historyStore.Record(p.Trace.TraceID, ar.AssertionID, meta.assertionType, ar.Score, ar.Status); recErr != nil {
					slog.Error("history store record error", "assertion_id", ar.AssertionID, "err", recErr)
				}

				// Report drift inside the response: stdout carries only
				// responses to requests the SDK sent.
				if meta.dynamic && ar.Status == types.StatusHardFail {
					mean, stddev, count, statsErr := historyStore.Stats(ar.AssertionID)
					if statsErr != nil {
						slog.Error("history store stats error", "assertion_id", ar.AssertionID, "err", statsErr)
						continue
					}
					ar.Drift = &types.DriftReport{
						AssertionID: ar.AssertionID,
						Mean:        mean,
						Stddev:      stddev,
						Count:       count,
						LatestScore: ar.Score,
						Deviation:   ar.Score - mean,
						Status:      "drift_detected",
					}
				}
			}
		}

		session.IncrementAssertions(len(result.Results))

		return &types.EvaluateBatchResult{
			Results:         result.Results,
			TotalCost:       result.TotalCost,
			TotalDurationMS: result.TotalDurationMS,
			Simulated:       false,
			Usage:           scope.Usage(),
		}, nil
	}
}

func handleQueryDrift(historyStore *cache.HistoryStore) Handler {
	return func(session *Session, params json.RawMessage) (any, *types.RPCError) {
		if session.State() != StateInitialized {
			return nil, types.NewRPCError(
				types.ErrSessionError,
				"query_drift called before initialize",
				types.ErrTypeSessionError,
				false,
				"call initialize first to establish a session",
			)
		}

		var p types.QueryDriftParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, types.NewRPCError(
				types.ErrAssertionError,
				"invalid query_drift params",
				types.ErrTypeAssertionError,
				false,
				err.Error(),
			)
		}

		if historyStore == nil {
			return nil, types.NewRPCError(
				types.ErrEngineError,
				"history store not available",
				types.ErrTypeEngineError,
				false,
				"history store failed to initialize at startup",
			)
		}

		windowSize := p.WindowSize
		if windowSize <= 0 {
			windowSize = 50
		}

		scores, err := historyStore.QueryWindow(p.AssertionID, windowSize)
		if err != nil {
			return nil, types.NewRPCError(
				types.ErrEngineError,
				fmt.Sprintf("query_drift failed: %v", err),
				types.ErrTypeEngineError,
				false,
				"error querying assertion history",
			)
		}

		mean, stddev, count, err := historyStore.Stats(p.AssertionID)
		if err != nil {
			return nil, types.NewRPCError(
				types.ErrEngineError,
				fmt.Sprintf("query_drift stats failed: %v", err),
				types.ErrTypeEngineError,
				false,
				"error computing assertion stats",
			)
		}

		var latestScore float64
		if len(scores) > 0 {
			latestScore = scores[0]
		}

		deviation := latestScore - mean

		report := types.DriftReport{
			AssertionID: p.AssertionID,
			Mean:        mean,
			Stddev:      stddev,
			Count:       count,
			LatestScore: latestScore,
			Deviation:   deviation,
			Status:      "ok",
		}
		if count > 0 && stddev > 0 && latestScore < mean-stddev {
			report.Status = "drift_detected"
		}

		return &types.QueryDriftResult{Report: report}, nil
	}
}

func handleSubmitPluginResult(historyStore *cache.HistoryStore) Handler {
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

		// E1: Record plugin result in history store.
		if historyStore != nil {
			if recErr := historyStore.Record(p.TraceID, p.AssertionID, "plugin", p.Result.Score, p.Result.Status); recErr != nil {
				slog.Error("history store record error", "assertion_id", p.AssertionID, "err", recErr)
			}
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
		if errors.Is(err, llm.ErrBudgetExhausted) {
			return nil, types.NewRPCError(
				types.ErrEngineError,
				fmt.Sprintf("generate_user_message not sent: %v", err),
				types.ErrTypeEngineError,
				false,
				"the session budget (ATTEST_MAX_JUDGE_CALLS, ATTEST_BUDGET_USD) is spent; raise it or shorten the conversation",
			)
		}
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
