package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/internal/assertion/embedding"
	"github.com/attest-ai/attest/engine/internal/llm"
	llmproviders "github.com/attest-ai/attest/engine/internal/llm/providers"
)

// judgeConfig is the Layer 6 judge configuration resolved from the environment.
type judgeConfig struct {
	provider  string // openai, anthropic, gemini or acp; empty when no judge is configured
	model     string // ATTEST_JUDGE_MODEL; empty selects the provider default (openai only)
	baseURL   string // provider API root, without trailing slash
	apiKey    string
	keySource string // name of the variable apiKey came from
	insecure  bool   // plain http to a non-loopback host: the key travels in cleartext
	// acpCommand is ATTEST_JUDGE_ACP_COMMAND, set only for provider acp,
	// which uses no base URL or API key.
	acpCommand string
}

// cacheEndpoint scopes judge cache entries by provider and base URL, so two
// providers behind the same gateway with the same model name never share
// scores; the acp judge is scoped by its agent command (never printed).
func (c judgeConfig) cacheEndpoint() string {
	if c.provider == "acp" {
		return "acp:" + c.acpCommand
	}
	return c.provider + "|" + c.baseURL
}

// judgeBackend describes one native judge provider family.
type judgeBackend struct {
	defaultBaseURL string
	standardKeys   []string // provider-standard key variables, in precedence order
	requiresModel  bool     // no built-in default model: ATTEST_JUDGE_MODEL is required
}

// judgeBackends lists the native judge providers. "openai" also covers any
// OpenAI-compatible endpoint selected with ATTEST_JUDGE_BASE_URL.
var judgeBackends = map[string]judgeBackend{
	"openai":    {defaultBaseURL: llm.OpenAIDefaultBaseURL, standardKeys: []string{"OPENAI_API_KEY"}},
	"anthropic": {defaultBaseURL: llmproviders.AnthropicDefaultBaseURL, standardKeys: []string{"ANTHROPIC_API_KEY"}, requiresModel: true},
	"gemini":    {defaultBaseURL: llmproviders.GeminiDefaultBaseURL, standardKeys: []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}, requiresModel: true},
}

// supportedJudgeProviders is the ATTEST_JUDGE_PROVIDER list shown in errors.
const supportedJudgeProviders = "openai, anthropic, gemini, acp"

// IsJudgeProvider reports whether name is a supported ATTEST_JUDGE_PROVIDER
// value. calibrate uses it so stored labels match judge_metadata.provider.
func IsJudgeProvider(name string) bool {
	_, ok := judgeBackends[name]
	return ok || name == "acp"
}

// SupportedJudgeProviders returns the supported provider names for messages.
func SupportedJudgeProviders() string { return supportedJudgeProviders }

// resolveJudgeConfig reads ATTEST_JUDGE_PROVIDER (default openai),
// ATTEST_JUDGE_MODEL, ATTEST_JUDGE_BASE_URL, ATTEST_JUDGE_API_KEY and the
// selected provider's standard key variables. A config with an empty
// provider means no judge is configured.
//
// ATTEST_JUDGE_API_KEY overrides the provider's standard variable
// (OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY then GEMINI_API_KEY,
// the precedence of Google's own SDK). With
// ATTEST_JUDGE_BASE_URL set, only ATTEST_JUDGE_API_KEY is used, so an
// exported provider key is never sent to a custom endpoint. Errors never
// include the API key or the base URL, which may embed credentials.
func resolveJudgeConfig() (judgeConfig, error) {
	name := os.Getenv("ATTEST_JUDGE_PROVIDER")
	if name == "" {
		name = "openai"
	}
	if name == "acp" {
		return resolveACPJudgeConfig()
	}
	backend, ok := judgeBackends[name]
	if !ok {
		return judgeConfig{}, fmt.Errorf("ATTEST_JUDGE_PROVIDER=%q is not supported; supported: %s", name, supportedJudgeProviders)
	}

	cfg := judgeConfig{model: os.Getenv("ATTEST_JUDGE_MODEL"), baseURL: backend.defaultBaseURL}
	keyVars := append([]string{"ATTEST_JUDGE_API_KEY"}, backend.standardKeys...)
	if raw := os.Getenv("ATTEST_JUDGE_BASE_URL"); raw != "" {
		u, err := parseJudgeBaseURL(raw)
		if err != nil {
			return judgeConfig{}, err
		}
		cfg.baseURL = u
		cfg.insecure = isInsecureEndpoint(u)
		keyVars = keyVars[:1]
		if os.Getenv("ATTEST_JUDGE_API_KEY") == "" {
			return judgeConfig{}, fmt.Errorf(
				"ATTEST_JUDGE_BASE_URL is set without ATTEST_JUDGE_API_KEY: %s is not sent to "+
					"a custom endpoint; set ATTEST_JUDGE_API_KEY (any non-empty value for an endpoint without authentication)",
				strings.Join(backend.standardKeys, " or "))
		}
	}

	for _, v := range keyVars {
		if key := os.Getenv(v); key != "" {
			cfg.apiKey, cfg.keySource = key, v
			break
		}
	}
	if cfg.apiKey == "" {
		return cfg, nil
	}
	if backend.requiresModel && cfg.model == "" {
		return judgeConfig{}, fmt.Errorf("ATTEST_JUDGE_PROVIDER=%s requires ATTEST_JUDGE_MODEL: this provider has no built-in default model", name)
	}
	cfg.provider = name
	return cfg, nil
}

// acpCommandName is the base name of the agent executable: the only part of
// ATTEST_JUDGE_ACP_COMMAND that is ever logged, because its arguments may
// be sensitive.
func (c judgeConfig) acpCommandName() string {
	fields := strings.Fields(c.acpCommand)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

// resolveACPJudgeConfig configures the local ACP judge. It takes no key and
// no base URL; the command, and the refusal under CI, are checked when the
// provider is built.
func resolveACPJudgeConfig() (judgeConfig, error) {
	if os.Getenv("ATTEST_JUDGE_BASE_URL") != "" {
		return judgeConfig{}, fmt.Errorf("ATTEST_JUDGE_BASE_URL does not apply to ATTEST_JUDGE_PROVIDER=acp; unset it")
	}
	return judgeConfig{
		provider:   "acp",
		model:      os.Getenv("ATTEST_JUDGE_MODEL"),
		acpCommand: os.Getenv("ATTEST_JUDGE_ACP_COMMAND"),
	}, nil
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
	u.Host = strings.ToLower(u.Host)
	return strings.TrimRight(u.String(), "/"), nil
}

// insecureNote is shown by doctor for a plain-http, non-loopback judge endpoint.
const insecureNote = "insecure: plain http to non-loopback host"

// isInsecureEndpoint reports whether baseURL (already validated) uses plain
// http to a host other than localhost or a loopback IP. Such endpoints stay
// allowed for internal servers but are reported at startup and by doctor.
func isInsecureEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// providers holds the Layer 5/6 providers configured from the environment.
type providers struct {
	embeddingRequested string // ATTEST_EMBEDDING_PROVIDER: auto, openai or onnx
	embeddingProvider  string // provider actually constructed; empty when none
	embedder           embedding.Embedder
	judgeConfig        judgeConfig
	judge              llm.Provider // nil when no judge is configured
	// ledger reserves every paid L5/L6 call; it is always set so usage is
	// counted even without limits.
	ledger *assertion.Ledger
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
	budget, err := resolveBudgetConfig()
	if err != nil {
		return p, err
	}
	p.ledger = assertion.NewLedger(budget.limits)

	p.embeddingRequested = requested
	p.embedder, p.embeddingProvider = buildEmbedder(logger, requested)
	var paidEmbedder *assertion.BudgetedEmbedder
	if ue, ok := p.embedder.(assertion.UsageEmbedder); ok {
		paidEmbedder = assertion.NewBudgetedEmbedder(ue, p.ledger)
		p.embedder = paidEmbedder
	}

	cfg, err := resolveJudgeConfig()
	if err != nil {
		return p, err
	}
	p.judgeConfig = cfg
	judgeModel := ""
	if cfg.provider != "" {
		judge, err := buildJudgeProvider(logger, cfg, p.ledger, budget.priceOverride)
		if err != nil {
			return p, err
		}
		p.judge = judge
		judgeModel = judge.DefaultModel()
		if cfg.provider == "acp" {
			logger.Warn("acp judge: the agent is not sandboxed; it runs as you with your network access and its own "+
				"configuration, so tools it may use without asking can act on untrusted agent output in judge prompts",
				"command", cfg.acpCommandName())
		}
		if cfg.insecure {
			logger.Warn("insecure judge endpoint: plain http to a non-loopback host sends ATTEST_JUDGE_API_KEY in cleartext",
				"base_url", cfg.baseURL)
		}
	}
	if err := checkBudgetPricing(budget, cfg.provider, judgeModel, paidEmbedder); err != nil {
		return p, err
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

// buildJudgeProvider constructs the judge provider selected by cfg and wraps
// it with that provider's rate limiter (ATTEST_RATE_LIMIT_<PROVIDER>).
func buildJudgeProvider(logger *slog.Logger, cfg judgeConfig, ledger *assertion.Ledger, price *llm.Price) (llm.Provider, error) {
	var p llm.Provider
	var err error
	switch cfg.provider {
	case "openai":
		p, err = llm.NewOpenAIProvider(cfg.apiKey, cfg.model, cfg.baseURL)
	case "anthropic":
		p, err = llmproviders.NewAnthropic(cfg.apiKey, cfg.model, cfg.baseURL)
	case "gemini":
		p, err = llmproviders.NewGemini(cfg.apiKey, cfg.model, cfg.baseURL)
	case "acp":
		p, err = llmproviders.NewACP(cfg.acpCommand, cfg.model, assertion.JudgeTimeout())
	default:
		return nil, fmt.Errorf("judge provider %q has no constructor", cfg.provider)
	}
	if err != nil {
		return nil, err
	}
	// The ledger wraps the concrete provider, inside the rate limiter, so
	// each retry reserves as its own call.
	p = assertion.NewBudgetedProvider(p, ledger, price)

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
