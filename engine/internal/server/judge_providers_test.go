package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// nativeJudge describes how one native provider's HTTP API looks on the
// wire, so the same scenarios run against an httptest fake of each.
type nativeJudge struct {
	provider  string
	keyVar    string // provider-standard key variable
	path      string // request path for model "judge-model"
	keyHeader string // header carrying the API key
	// checkBody validates the decoded request body; empty string means OK.
	checkBody func(map[string]any) string
	// success is a 2xx body whose text is the judge's JSON verdict.
	success string
	// rateLimited and outage are provider-shaped error bodies.
	rateLimited, outage string
	outageStatus        int
}

const judgeVerdict = `{\"score\": 0.95, \"explanation\": \"helpful answer\"}`

var nativeJudges = []nativeJudge{
	{
		provider:  "anthropic",
		keyVar:    "ANTHROPIC_API_KEY",
		path:      "/v1/messages",
		keyHeader: "x-api-key",
		checkBody: func(b map[string]any) string {
			if b["model"] != "judge-model" {
				return "model"
			}
			if s, _ := b["system"].(string); s == "" {
				return "system prompt missing"
			}
			if mt, _ := b["max_tokens"].(float64); mt <= 0 {
				return "max_tokens missing"
			}
			msgs, _ := b["messages"].([]any)
			if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
				return "messages"
			}
			return ""
		},
		success: `{"id":"msg_1","type":"message","role":"assistant","model":"judge-model",` +
			`"content":[{"type":"text","text":"` + judgeVerdict + `"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":8}}`,
		rateLimited:  `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		outage:       `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		outageStatus: 529,
	},
	{
		provider:  "gemini",
		keyVar:    "GEMINI_API_KEY",
		path:      "/v1beta/models/judge-model:generateContent",
		keyHeader: "x-goog-api-key",
		checkBody: func(b map[string]any) string {
			si, _ := b["systemInstruction"].(map[string]any)
			if parts, _ := si["parts"].([]any); len(parts) != 1 {
				return "systemInstruction missing"
			}
			contents, _ := b["contents"].([]any)
			if len(contents) != 1 || contents[0].(map[string]any)["role"] != "user" {
				return "contents"
			}
			return ""
		},
		success: `{"candidates":[{"content":{"role":"model","parts":[{"text":"` + judgeVerdict + `"}]},` +
			`"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":8},` +
			`"modelVersion":"judge-model"}`,
		rateLimited:  `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`,
		outage:       `{"error":{"code":503,"message":"The model is overloaded","status":"UNAVAILABLE"}}`,
		outageStatus: http.StatusServiceUnavailable,
	},
}

// fakeNativeJudge serves the provider's API. respond(n) returns the status
// and body for the n-th (1-based) well-formed request; malformed requests
// get 400 and are not counted.
func fakeNativeJudge(t *testing.T, j nativeJudge, respond func(n int32) (int, string)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != j.path {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		if got := r.Header.Get(j.keyHeader); got != "judge-key" {
			http.Error(w, "unexpected key header", http.StatusUnauthorized)
			return
		}
		if r.URL.RawQuery != "" {
			http.Error(w, "the key must not travel in the query string", http.StatusBadRequest)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if bad := j.checkBody(body); bad != "" {
			http.Error(w, "bad request body: "+bad, http.StatusBadRequest)
			return
		}
		status, out := respond(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func configureNativeJudge(t *testing.T, j nativeJudge, srv *httptest.Server) {
	t.Helper()
	withoutProviders(t, "")
	t.Setenv("ATTEST_JUDGE_PROVIDER", j.provider)
	t.Setenv("ATTEST_JUDGE_BASE_URL", srv.URL)
	t.Setenv("ATTEST_JUDGE_API_KEY", "judge-key")
	t.Setenv(j.keyVar, "standard-key-must-not-reach-custom-endpoint")
	t.Setenv("ATTEST_JUDGE_MODEL", "judge-model")
}

func TestHandler_NativeJudge_EvaluatesAndRecordsProviderAndModel(t *testing.T) {
	for _, j := range nativeJudges {
		t.Run(j.provider, func(t *testing.T) {
			srv, calls := fakeNativeJudge(t, j, func(int32) (int, string) { return http.StatusOK, j.success })
			configureNativeJudge(t, j, srv)

			_, r := evaluateJudgeOnly(t)

			if r.Status != types.StatusPass {
				t.Fatalf("status = %q (explanation %q), want pass", r.Status, r.Explanation)
			}
			if r.Score != 0.95 {
				t.Errorf("score = %v, want 0.95", r.Score)
			}
			if calls.Load() != 1 {
				t.Errorf("fake received %d requests, want 1", calls.Load())
			}
			if r.Judge == nil || r.Judge.Provider != j.provider || r.Judge.Model != "judge-model" {
				t.Errorf("judge_metadata = %+v, want provider %q and model judge-model", r.Judge, j.provider)
			}
		})
	}
}

func TestHandler_NativeJudge_RetriesRateLimitedRequest(t *testing.T) {
	for _, j := range nativeJudges {
		t.Run(j.provider, func(t *testing.T) {
			srv, calls := fakeNativeJudge(t, j, func(n int32) (int, string) {
				if n == 1 {
					return http.StatusTooManyRequests, j.rateLimited
				}
				return http.StatusOK, j.success
			})
			configureNativeJudge(t, j, srv)
			t.Setenv("ATTEST_RATE_LIMIT_"+strings.ToUpper(j.provider), "600")
			t.Setenv("ATTEST_RATE_LIMIT_"+strings.ToUpper(j.provider)+"_BURST", "2")

			_, r := evaluateJudgeOnly(t)

			if r.Status != types.StatusPass {
				t.Fatalf("status = %q (explanation %q), want pass after a 429 retry", r.Status, r.Explanation)
			}
			if calls.Load() != 2 {
				t.Errorf("fake received %d requests, want 2 (one 429, one retry)", calls.Load())
			}
		})
	}
}

func TestHandler_NativeJudge_OutageIsNotEvaluatedProviderUnavailable(t *testing.T) {
	for _, j := range nativeJudges {
		t.Run(j.provider, func(t *testing.T) {
			srv, calls := fakeNativeJudge(t, j, func(int32) (int, string) { return j.outageStatus, j.outage })
			configureNativeJudge(t, j, srv)

			_, r := evaluateJudgeOnly(t)

			if r.Status != types.StatusNotEvaluated || r.Reason != types.ReasonProviderUnavailable {
				t.Fatalf("status = %q reason = %q, want not_evaluated/provider_unavailable", r.Status, r.Reason)
			}
			if r.FailureClass != "" {
				t.Errorf("failure_class = %q; C1 sets it only on failures", r.FailureClass)
			}
			if calls.Load() < 2 {
				t.Errorf("fake received %d requests, want the rate limiter to retry", calls.Load())
			}
			if strings.Contains(r.Explanation, "judge-key") || strings.Contains(r.Explanation, "standard-key") {
				t.Errorf("explanation leaks an API key: %q", r.Explanation)
			}
		})
	}
}

func TestBuildRateLimiterConfig_ReadsPerProviderVariables(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			for _, p := range []string{"OPENAI", "ANTHROPIC", "GEMINI"} {
				t.Setenv("ATTEST_RATE_LIMIT_"+p, "7")
				t.Setenv("ATTEST_RATE_LIMIT_"+p+"_BURST", "1")
			}
			upper := strings.ToUpper(provider)
			t.Setenv("ATTEST_RATE_LIMIT_"+upper, "120")
			t.Setenv("ATTEST_RATE_LIMIT_"+upper+"_BURST", "4")

			cfg, err := buildRateLimiterConfig(provider)

			if err != nil {
				t.Fatal(err)
			}
			if cfg.RequestsPerMinute != 120 || cfg.Burst != 4 {
				t.Errorf("got rpm=%v burst=%d, want 120/4 from ATTEST_RATE_LIMIT_%s", cfg.RequestsPerMinute, cfg.Burst, upper)
			}
		})
	}
}

func TestResolveJudgeConfig_NativeProviderStandardKeys(t *testing.T) {
	cases := []struct {
		provider, keyVar, wantBaseURL string
	}{
		{"anthropic", "ANTHROPIC_API_KEY", "https://api.anthropic.com"},
		{"gemini", "GEMINI_API_KEY", "https://generativelanguage.googleapis.com"},
		{"gemini", "GOOGLE_API_KEY", "https://generativelanguage.googleapis.com"},
	}
	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.keyVar, func(t *testing.T) {
			withoutProviders(t, "")
			t.Setenv("ATTEST_JUDGE_PROVIDER", tc.provider)
			t.Setenv("ATTEST_JUDGE_MODEL", "judge-model")
			t.Setenv("OPENAI_API_KEY", "openai-key-must-not-be-used")
			t.Setenv(tc.keyVar, "std-key")

			cfg, err := resolveJudgeConfig()

			if err != nil {
				t.Fatalf("resolveJudgeConfig: %v", err)
			}
			if cfg.provider != tc.provider || cfg.apiKey != "std-key" || cfg.keySource != tc.keyVar {
				t.Errorf("got provider=%q key=%q source=%q", cfg.provider, cfg.apiKey, cfg.keySource)
			}
			if cfg.baseURL != tc.wantBaseURL {
				t.Errorf("baseURL = %q, want %q", cfg.baseURL, tc.wantBaseURL)
			}
		})
	}
}
