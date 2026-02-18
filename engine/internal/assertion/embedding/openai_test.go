package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newOpenAITestServer(t *testing.T, statusCode int, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
}

func TestOpenAIEmbedder_Success(t *testing.T) {
	srv := newOpenAITestServer(t, http.StatusOK, map[string]any{
		"data": []map[string]any{
			{"embedding": []float32{0.1, 0.2, 0.3}},
		},
	})
	defer srv.Close()

	embedder, err := NewOpenAIEmbedder(EmbedderConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	vec, err := embedder.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("expected 3 dimensions, got %d", len(vec))
	}
	if vec[0] != 0.1 || vec[1] != 0.2 || vec[2] != 0.3 {
		t.Errorf("unexpected vector: %v", vec)
	}
}

func TestOpenAIEmbedder_DefaultModel(t *testing.T) {
	embedder, err := NewOpenAIEmbedder(EmbedderConfig{APIKey: "key"})
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	if embedder.Model() != "text-embedding-3-small" {
		t.Errorf("default model: got %q, want text-embedding-3-small", embedder.Model())
	}
}

func TestOpenAIEmbedder_CustomModel(t *testing.T) {
	embedder, err := NewOpenAIEmbedder(EmbedderConfig{
		APIKey: "key",
		Model:  "text-embedding-3-large",
	})
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	if embedder.Model() != "text-embedding-3-large" {
		t.Errorf("model: got %q, want text-embedding-3-large", embedder.Model())
	}
}

func TestOpenAIEmbedder_MissingAPIKey(t *testing.T) {
	_, err := NewOpenAIEmbedder(EmbedderConfig{})
	if err == nil {
		t.Fatal("expected error for missing API key, got nil")
	}
}

func TestOpenAIEmbedder_APIError(t *testing.T) {
	srv := newOpenAITestServer(t, http.StatusUnauthorized, map[string]any{
		"error": map[string]any{
			"message": "invalid api key",
			"type":    "invalid_request_error",
		},
	})
	defer srv.Close()

	embedder, err := NewOpenAIEmbedder(EmbedderConfig{
		APIKey:  "bad-key",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	_, err = embedder.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for API error response, got nil")
	}
}

func TestOpenAIEmbedder_EmptyEmbedding(t *testing.T) {
	srv := newOpenAITestServer(t, http.StatusOK, map[string]any{
		"data": []map[string]any{},
	})
	defer srv.Close()

	embedder, err := NewOpenAIEmbedder(EmbedderConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	_, err = embedder.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for empty embedding response, got nil")
	}
}

func TestOpenAIEmbedder_RateLimitReturnsError(t *testing.T) {
	srv := newOpenAITestServer(t, http.StatusTooManyRequests, map[string]any{
		"error": map[string]any{
			"message": "rate limit exceeded",
			"type":    "rate_limit_error",
		},
	})
	defer srv.Close()

	embedder, err := NewOpenAIEmbedder(EmbedderConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	_, err = embedder.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for rate limit response, got nil")
	}
}

func TestOpenAIEmbedder_ContextCancellation(t *testing.T) {
	// Server that blocks until request context is cancelled
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	embedder, err := NewOpenAIEmbedder(EmbedderConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err = embedder.Embed(ctx, "test")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}
