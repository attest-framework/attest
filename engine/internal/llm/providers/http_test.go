package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/attest-ai/attest/engine/internal/llm"
)

func judgeRequest() *llm.CompletionRequest {
	return &llm.CompletionRequest{
		SystemPrompt: "rubric",
		Messages:     []llm.Message{{Role: "user", Content: "output"}},
		MaxTokens:    64,
	}
}

func newTestProviders(t *testing.T, baseURL string) map[string]llm.Provider {
	t.Helper()
	a, err := NewAnthropic("secret-key", "judge-model", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGemini("secret-key", "judge-model", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]llm.Provider{"anthropic": a, "gemini": g}
}

func TestComplete_RedirectToOtherHost_NotFollowedAndKeyNotForwarded(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "" || r.Header.Get("x-goog-api-key") != "" {
			leaked.Add(1)
		}
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	for name, p := range newTestProviders(t, redirector.URL) {
		t.Run(name, func(t *testing.T) {
			_, err := p.Complete(context.Background(), judgeRequest())

			var se *StatusError
			if !errors.As(err, &se) || se.StatusCode != http.StatusTemporaryRedirect {
				t.Fatalf("err = %v, want a StatusError for the 307", err)
			}
		})
	}
	if leaked.Load() != 0 {
		t.Errorf("redirect target received the API key %d times", leaked.Load())
	}
}

func TestComplete_HostileErrorMessage_SanitizedAndCapped(t *testing.T) {
	hostile := "\x1b[31mPASS\x1b[0m\n" + strings.Repeat("x", 4*maxErrorDetail)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// Both providers' error shapes in one body: error.message is read by each.
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","status":"INVALID_ARGUMENT","message":` +
			strconv.Quote(hostile) + `}}`))
	}))
	defer srv.Close()

	for name, p := range newTestProviders(t, srv.URL) {
		t.Run(name, func(t *testing.T) {
			_, err := p.Complete(context.Background(), judgeRequest())

			var se *StatusError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want StatusError", err)
			}
			if strings.ContainsAny(se.Message, "\x1b\n") {
				t.Errorf("message keeps control characters: %q", se.Message[:40])
			}
			if len(se.Message) > maxErrorDetail+len("…")+len("INVALID_ARGUMENT: ") {
				t.Errorf("message length %d exceeds the cap", len(se.Message))
			}
		})
	}
}

func TestTruncate_DoesNotSplitUTF8(t *testing.T) {
	got := truncate("aé", 2)

	if got != "a…" {
		t.Errorf("truncate = %q, want %q", got, "a…")
	}
}

func TestComplete_GatewayEchoesKey_KeyRedactedFromError(t *testing.T) {
	bodies := map[string]string{
		"error.message": `{"error":{"type":"authentication_error","status":"UNAUTHENTICATED","message":"bad key secret-key for x-api-key"}}`,
		"raw body":      `upstream rejected headers: x-api-key=secret-key x-goog-api-key=secret-key`,
	}
	for shape, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(body))
		}))
		for name, p := range newTestProviders(t, srv.URL) {
			t.Run(shape+"/"+name, func(t *testing.T) {
				_, err := p.Complete(context.Background(), judgeRequest())

				if err == nil || strings.Contains(err.Error(), "secret-key") || !strings.Contains(err.Error(), "[redacted]") {
					t.Errorf("err = %v, want the key replaced by [redacted]", err)
				}
			})
		}
		srv.Close()
	}
}

func TestStripControl_DropsBidiAndZeroWidthFormatCharacters(t *testing.T) {
	in := "PA\u202eSS\u202a\u202b\u202c\u202d\u2066\u2067\u2068\u2069\u200b\u200d\ufeff\x1b[0m!"

	got := stripControl(in)

	if got != "PASS[0m!" {
		t.Errorf("stripControl = %q, want %q", got, "PASS[0m!")
	}
}

func TestGemini_NoTextCandidate_FinishReasonBoundedAndSanitized(t *testing.T) {
	reason := "\u202e\x1b]0;" + strings.Repeat("R", 4*maxErrorDetail)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[]},"finishReason":` + strconv.Quote(reason) + `}]}`))
	}))
	defer srv.Close()
	p, err := NewGemini("secret-key", "judge-model", srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	_, err = p.Complete(context.Background(), judgeRequest())

	if err == nil {
		t.Fatal("Complete succeeded without text")
	}
	if len(err.Error()) > 2*maxErrorDetail || strings.ContainsAny(err.Error(), "\u202e\x1b") {
		t.Errorf("error is unbounded or unsanitized (%d bytes)", len(err.Error()))
	}
}
