package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/attest-ai/attest/engine/internal/llm"
)

func TestGemini_ThoughtsTokens_CountAsOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"{\"score\":0.9,\"explanation\":\"ok\"}"}]},"finishReason":"STOP"}],`+
			`"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":300}}`)
	}))
	defer srv.Close()
	p, err := NewGemini("key", "gemini-test", srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.Complete(context.Background(), &llm.CompletionRequest{Messages: []llm.Message{{Role: "user", Content: "x"}}})

	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputTokens != 320 {
		t.Errorf("OutputTokens = %d, want candidates 20 + thoughts 300", resp.OutputTokens)
	}
}
