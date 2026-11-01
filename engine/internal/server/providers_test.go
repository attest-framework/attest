package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// TestHandler_JudgeBaseURL_EvaluatesL6AgainstCompatibleServer runs an L6
// assertion end to end through RegisterBuiltinHandlers against an
// OpenAI-compatible httptest server selected by ATTEST_JUDGE_BASE_URL.
func TestHandler_JudgeBaseURL_EvaluatesL6AgainstCompatibleServer(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer judge-key" {
			http.Error(w, "unexpected authorization "+got, http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.Model != "local-judge" {
			http.Error(w, "unexpected model in "+string(body), http.StatusBadRequest)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"local-judge","choices":[{"message":{"role":"assistant",`+
			`"content":"{\"score\": 0.95, \"explanation\": \"helpful answer\"}"}}],`+
			`"usage":{"prompt_tokens":12,"completion_tokens":8}}`)
	}))
	defer srv.Close()

	withoutProviders(t, "")
	t.Setenv("ATTEST_JUDGE_BASE_URL", srv.URL+"/v1/")
	t.Setenv("ATTEST_JUDGE_API_KEY", "judge-key")
	t.Setenv("OPENAI_API_KEY", "standard-key-must-not-reach-judge")
	t.Setenv("ATTEST_JUDGE_MODEL", "local-judge")

	initRes, r := evaluateJudgeOnly(t)

	if !slices.Contains(initRes.Capabilities, "layers_5_6") {
		t.Errorf("capabilities %v missing layers_5_6 with a judge configured", initRes.Capabilities)
	}
	if r.Status != types.StatusPass {
		t.Fatalf("status = %q (explanation %q), want pass", r.Status, r.Explanation)
	}
	if r.Score != 0.95 {
		t.Errorf("score = %v, want 0.95 from the compatible server", r.Score)
	}
	if calls.Load() == 0 {
		t.Error("judge never called the ATTEST_JUDGE_BASE_URL server")
	}
}

func TestResolveJudgeConfig_KeyPrecedence(t *testing.T) {
	cases := []struct {
		name, judgeKey, standardKey, wantKey, wantSource, wantProvider string
	}{
		{"override wins", "attest-key", "std-key", "attest-key", "ATTEST_JUDGE_API_KEY", "openai"},
		{"standard variable", "", "std-key", "std-key", "OPENAI_API_KEY", "openai"},
		{"no key", "", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withoutProviders(t, "")
			t.Setenv("ATTEST_JUDGE_API_KEY", tc.judgeKey)
			t.Setenv("OPENAI_API_KEY", tc.standardKey)

			cfg, err := resolveJudgeConfig()

			if err != nil {
				t.Fatalf("resolveJudgeConfig: %v", err)
			}
			if cfg.apiKey != tc.wantKey || cfg.keySource != tc.wantSource || cfg.provider != tc.wantProvider {
				t.Errorf("got key=%q source=%q provider=%q, want %q %q %q",
					cfg.apiKey, cfg.keySource, cfg.provider, tc.wantKey, tc.wantSource, tc.wantProvider)
			}
			if cfg.baseURL != llm.OpenAIDefaultBaseURL {
				t.Errorf("baseURL = %q, want default %q", cfg.baseURL, llm.OpenAIDefaultBaseURL)
			}
		})
	}
}

func TestResolveJudgeConfig_RemovedLegacyKeyIgnored(t *testing.T) {
	withoutProviders(t, "")
	t.Setenv("ATTEST_OPENAI_API_KEY", "legacy-key")

	cfg, err := resolveJudgeConfig()

	if err != nil {
		t.Fatalf("resolveJudgeConfig: %v", err)
	}
	if cfg.provider != "" || cfg.apiKey != "" {
		t.Errorf("ATTEST_OPENAI_API_KEY configured a judge (provider=%q); it is removed", cfg.provider)
	}
}

func TestResolveJudgeConfig_InvalidSettingsError(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		wantPart string
	}{
		{"unimplemented provider", map[string]string{"ATTEST_JUDGE_PROVIDER": "anthropic"}, "supported: openai"},
		{"unknown provider", map[string]string{"ATTEST_JUDGE_PROVIDER": "bogus"}, "supported: openai"},
		{"base URL without key", map[string]string{"ATTEST_JUDGE_BASE_URL": "http://localhost:8080/v1"}, "ATTEST_JUDGE_API_KEY"},
		{"base URL with only standard key", map[string]string{"ATTEST_JUDGE_BASE_URL": "https://other.example/v1", "OPENAI_API_KEY": "secret-key"}, "OPENAI_API_KEY is not sent"},
		{"credentials with bad scheme", map[string]string{"ATTEST_JUDGE_BASE_URL": "ftp://user:pw@host/v1", "ATTEST_JUDGE_API_KEY": "secret-key"}, "must not contain credentials"},
		{"credentials with no host", map[string]string{"ATTEST_JUDGE_BASE_URL": "http://user:pw@/v1", "ATTEST_JUDGE_API_KEY": "secret-key"}, "must not contain credentials"},
		{"malformed URL", map[string]string{"ATTEST_JUDGE_BASE_URL": "http://user:pw@host:bad port/v1", "ATTEST_JUDGE_API_KEY": "secret-key"}, "not a valid URL"},
		{"empty query", map[string]string{"ATTEST_JUDGE_BASE_URL": "https://host/v1?", "ATTEST_JUDGE_API_KEY": "secret-key"}, "query or fragment"},
		{"key in query", map[string]string{"ATTEST_JUDGE_BASE_URL": "https://host/v1?api-key=pw", "ATTEST_JUDGE_API_KEY": "secret-key"}, "query or fragment"},
		{"non-http scheme", map[string]string{"ATTEST_JUDGE_BASE_URL": "ftp://host/v1", "ATTEST_JUDGE_API_KEY": "secret-key"}, "http or https"},
		{"missing host", map[string]string{"ATTEST_JUDGE_BASE_URL": "http:///v1", "ATTEST_JUDGE_API_KEY": "secret-key"}, "no host"},
		{"credentials in URL", map[string]string{"ATTEST_JUDGE_BASE_URL": "https://user:pw@host/v1", "ATTEST_JUDGE_API_KEY": "secret-key"}, "must not contain credentials"},
		{"query string", map[string]string{"ATTEST_JUDGE_BASE_URL": "https://host/v1?x=1", "ATTEST_JUDGE_API_KEY": "secret-key"}, "query or fragment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withoutProviders(t, "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			_, err := resolveJudgeConfig()

			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantPart) {
				t.Errorf("error %q does not contain %q", err, tc.wantPart)
			}
			if strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "pw") {
				t.Errorf("error leaks a credential: %q", err)
			}
			if strings.Contains(err.Error(), "anthropic,") || strings.Contains(err.Error(), "gemini") {
				t.Errorf("error lists unimplemented providers: %q", err)
			}
		})
	}
}

func TestBuildProviders_RejectsUnknownEmbeddingProvider(t *testing.T) {
	withoutProviders(t, "")
	t.Setenv("ATTEST_EMBEDDING_PROVIDER", "cohere")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := buildProviders(logger)

	if err == nil || !strings.Contains(err.Error(), "ATTEST_EMBEDDING_PROVIDER") {
		t.Fatalf("err = %v, want ATTEST_EMBEDDING_PROVIDER error", err)
	}
}

func TestBuildRateLimiterConfig_RejectsNonPositive(t *testing.T) {
	for _, key := range []string{"ATTEST_RATE_LIMIT_OPENAI", "ATTEST_RATE_LIMIT_OPENAI_BURST"} {
		for _, v := range []string{"0", "-1"} {
			t.Run(key+"="+v, func(t *testing.T) {
				t.Setenv("ATTEST_RATE_LIMIT_OPENAI", "")
				t.Setenv("ATTEST_RATE_LIMIT_OPENAI_BURST", "")
				t.Setenv(key, v)

				_, err := buildRateLimiterConfig("openai")

				if err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("err = %v, want error naming %s", err, key)
				}
			})
		}
	}
}

func TestDiagnose_RejectsSameEnvAsServer(t *testing.T) {
	cases := map[string]string{
		"ATTEST_EVAL_CONCURRENCY":       "abc",
		"ATTEST_MAX_SOFT_FAILURES":      "-1",
		"ATTEST_EMBEDDING_CACHE_MAX_MB": "x",
		"ATTEST_JUDGE_CACHE_MAX_MB":     "x",
		"ATTEST_HISTORY_MAX_ROWS":       "x",
		"ATTEST_HISTORY_MAX_AGE_DAYS":   "x",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for key, v := range cases {
		t.Run(key, func(t *testing.T) {
			withoutProviders(t, "")
			t.Setenv(key, v)

			_, diagErr := Diagnose(logger)
			regErr := RegisterBuiltinHandlers(New(nil, nil, logger))

			if diagErr == nil || regErr == nil {
				t.Fatalf("doctor err = %v, server err = %v; want both to reject %s=%s", diagErr, regErr, key, v)
			}
			if diagErr.Error() != regErr.Error() {
				t.Errorf("doctor %q != server %q", diagErr, regErr)
			}
		})
	}
}

func TestResolveJudgeConfig_NormalizesBaseURL(t *testing.T) {
	withoutProviders(t, "")
	t.Setenv("ATTEST_JUDGE_BASE_URL", "HTTP://LocalHost:8000/v1/")
	t.Setenv("ATTEST_JUDGE_API_KEY", "k")

	cfg, err := resolveJudgeConfig()

	if err != nil {
		t.Fatalf("resolveJudgeConfig: %v", err)
	}
	if cfg.baseURL != "http://localhost:8000/v1" {
		t.Errorf("baseURL = %q, want http://localhost:8000/v1", cfg.baseURL)
	}
}
