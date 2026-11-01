package server

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/attest-ai/attest/engine/internal/assertion/embedding"
	"github.com/attest-ai/attest/engine/internal/llm"
)

// judgeConfig is the Layer 6 judge configuration resolved from the environment.
type judgeConfig struct {
	provider  string // "openai"; empty when no API key is set
	model     string // ATTEST_JUDGE_MODEL; empty selects the provider default
	baseURL   string // OpenAI-compatible endpoint, without trailing slash
	apiKey    string
	keySource string // name of the variable apiKey came from
}

// resolveJudgeConfig reads ATTEST_JUDGE_PROVIDER, ATTEST_JUDGE_MODEL,
// ATTEST_JUDGE_BASE_URL, ATTEST_JUDGE_API_KEY and OPENAI_API_KEY. A config
// with an empty provider means no judge is configured.
//
// ATTEST_JUDGE_API_KEY overrides the provider's standard OPENAI_API_KEY. With
// ATTEST_JUDGE_BASE_URL set, only ATTEST_JUDGE_API_KEY is used, so an exported
// OPENAI_API_KEY is never sent to a custom endpoint. Errors never include the
// API key or the base URL, which may embed credentials.
func resolveJudgeConfig() (judgeConfig, error) {
	if p := os.Getenv("ATTEST_JUDGE_PROVIDER"); p != "" && p != "openai" {
		return judgeConfig{}, fmt.Errorf("ATTEST_JUDGE_PROVIDER=%q is not supported; supported: openai", p)
	}

	cfg := judgeConfig{model: os.Getenv("ATTEST_JUDGE_MODEL"), baseURL: llm.OpenAIDefaultBaseURL}
	keyVars := []string{"ATTEST_JUDGE_API_KEY", "OPENAI_API_KEY"}
	if raw := os.Getenv("ATTEST_JUDGE_BASE_URL"); raw != "" {
		u, err := parseJudgeBaseURL(raw)
		if err != nil {
			return judgeConfig{}, err
		}
		cfg.baseURL = u
		keyVars = keyVars[:1]
		if os.Getenv("ATTEST_JUDGE_API_KEY") == "" {
			return judgeConfig{}, fmt.Errorf(
				"ATTEST_JUDGE_BASE_URL is set without ATTEST_JUDGE_API_KEY: OPENAI_API_KEY is not sent to " +
					"a custom endpoint; set ATTEST_JUDGE_API_KEY (any non-empty value for an endpoint without authentication)")
		}
	}

	for _, name := range keyVars {
		if v := os.Getenv(name); v != "" {
			cfg.apiKey, cfg.keySource = v, name
			break
		}
	}
	if cfg.apiKey == "" {
		return cfg, nil
	}
	cfg.provider = "openai"
	return cfg, nil
}

// parseJudgeBaseURL validates an OpenAI-compatible base URL such as
// http://localhost:11434/v1. Credentials in the URL are rejected so they never
// reach logs or doctor output; the key belongs in ATTEST_JUDGE_API_KEY. Error
// messages never echo the URL.
func parseJudgeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("ATTEST_JUDGE_BASE_URL is not a valid URL")
	}
	if u.User != nil {
		return "", fmt.Errorf("ATTEST_JUDGE_BASE_URL must not contain credentials; set ATTEST_JUDGE_API_KEY instead")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("ATTEST_JUDGE_BASE_URL must use http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("ATTEST_JUDGE_BASE_URL has no host")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("ATTEST_JUDGE_BASE_URL must not contain a query or fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// providers holds the Layer 5/6 providers configured from the environment.
type providers struct {
	embeddingRequested string // ATTEST_EMBEDDING_PROVIDER: auto, openai or onnx
	embeddingProvider  string // provider actually constructed; empty when none
	embedder           embedding.Embedder
	judgeConfig        judgeConfig
	judge              llm.Provider // nil when no judge is configured
}

// buildProviders constructs the embedding and judge providers selected by the
// environment. It performs no network calls except the checksum-verified ONNX
// model download on first use in onnx builds. Returns an error when any
// explicitly-set variable has an invalid value.
func buildProviders(logger *slog.Logger) (providers, error) {
	var p providers

	requested := os.Getenv("ATTEST_EMBEDDING_PROVIDER")
	if requested == "" {
		requested = "auto"
	}
	if requested != "auto" && requested != "openai" && requested != "onnx" {
		return p, fmt.Errorf("ATTEST_EMBEDDING_PROVIDER=%q is not supported; supported: auto, openai, onnx", requested)
	}
	p.embeddingRequested = requested
	p.embedder, p.embeddingProvider = buildEmbedder(logger, requested)

	cfg, err := resolveJudgeConfig()
	if err != nil {
		return p, err
	}
	p.judgeConfig = cfg
	if cfg.provider != "" {
		judge, err := buildJudgeProvider(logger, cfg)
		if err != nil {
			return p, err
		}
		p.judge = judge
	}
	return p, nil
}

// buildEmbedder selects the Layer 5 embedder: OpenAI when OPENAI_API_KEY is
// set and requested is auto or openai; ONNX when requested is onnx, or auto
// without an OpenAI key. Construction failures are logged and leave L5
// unconfigured.
func buildEmbedder(logger *slog.Logger, requested string) (embedding.Embedder, string) {
	openAIKey := os.Getenv("OPENAI_API_KEY")
	if openAIKey != "" && (requested == "auto" || requested == "openai") {
		e, err := embedding.NewOpenAIEmbedder(embedding.EmbedderConfig{APIKey: openAIKey})
		if err != nil {
			logger.Warn("failed to create OpenAI embedder", "err", err)
		} else {
			return e, "openai"
		}
	}

	if requested == "onnx" || (requested == "auto" && openAIKey == "") {
		if embedding.ONNXAvailable {
			e, err := embedding.NewONNXEmbedder(embedding.EmbedderConfig{ModelDir: os.Getenv("ATTEST_ONNX_MODEL_DIR")})
			if err != nil {
				logger.Warn("failed to create ONNX embedder", "err", err)
				return nil, ""
			}
			return e, "onnx"
		}
		if requested == "onnx" {
			logger.Warn("ONNX embedding requested but not compiled in — rebuild with -tags onnx")
		}
	}
	return nil, ""
}

// buildJudgeProvider constructs the OpenAI-compatible judge provider for cfg
// and wraps it with the configured rate limiter.
func buildJudgeProvider(logger *slog.Logger, cfg judgeConfig) (llm.Provider, error) {
	p, err := llm.NewOpenAIProvider(cfg.apiKey, cfg.model, cfg.baseURL)
	if err != nil {
		return nil, err
	}

	rlCfg, err := buildRateLimiterConfig(cfg.provider)
	if err != nil {
		return nil, err
	}
	rlp, rlErr := llm.NewRateLimitedProvider(p, rlCfg)
	if rlErr != nil {
		logger.Warn("rate limiter init failed, using bare provider", "err", rlErr)
		return p, nil
	}
	logger.Info("judge provider rate limiter configured",
		"provider", cfg.provider, "rpm", rlCfg.RequestsPerMinute, "burst", rlCfg.Burst)
	return rlp, nil
}

// buildRateLimiterConfig resolves the rate-limiter config for a named provider
// from ATTEST_RATE_LIMIT_<PROVIDER> and ATTEST_RATE_LIMIT_<PROVIDER>_BURST,
// defaulting to llm.DefaultRateLimiterConfig. Returns an error when either
// variable is set to a non-integer or a value <= 0.
func buildRateLimiterConfig(provider string) (llm.RateLimiterConfig, error) {
	cfg := llm.DefaultRateLimiterConfig
	upper := strings.ToUpper(provider)

	rpm, err := positiveEnvInt("ATTEST_RATE_LIMIT_" + upper)
	if err != nil {
		return cfg, err
	}
	if rpm > 0 {
		cfg.RequestsPerMinute = float64(rpm)
	}

	burst, err := positiveEnvInt("ATTEST_RATE_LIMIT_" + upper + "_BURST")
	if err != nil {
		return cfg, err
	}
	if burst > 0 {
		cfg.Burst = burst
	}
	return cfg, nil
}

// positiveEnvInt reads a positive integer from key. Returns 0 when key is
// unset and an error when it is set to a non-integer or a value <= 0.
func positiveEnvInt(key string) (int, error) {
	n, err := envInt(key, 0)
	if err != nil {
		return 0, err
	}
	if os.Getenv(key) != "" && n <= 0 {
		return 0, fmt.Errorf("env var %s=%d must be > 0", key, n)
	}
	return n, nil
}

// capabilities returns the capability list `initialize` advertises for these
// providers. L5/L6 assertion types are always accepted: without a provider
// the pipeline returns not_evaluated(provider_not_configured) rows.
// `layers_5_6` means at least one provider is configured; `simulation_mode`
// means ATTEST_SIMULATION is set, so L5/L6 return not_evaluated(simulation)
// and no provider is called.
func (p providers) capabilities(simulation bool) []string {
	caps := []string{"layers_1_4", "trace_tree", "continuous_eval", "plugins"}
	if p.embedder != nil {
		caps = append(caps, "embedding")
	}
	if p.judge != nil {
		caps = append(caps, "llm_judge", "simulation")
	}
	if p.embedder != nil || p.judge != nil {
		caps = append(caps, "layers_5_6")
	}
	if p.embedder == nil {
		caps = append(caps, "embedding")
	}
	if p.judge == nil {
		caps = append(caps, "llm_judge")
	}
	if simulation {
		caps = append(caps, "simulation_mode")
	}
	return caps
}
