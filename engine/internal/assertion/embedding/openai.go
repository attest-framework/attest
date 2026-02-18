package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	openAIDefaultModel   = "text-embedding-3-small"
	openAIDefaultBaseURL = "https://api.openai.com/v1"
)

// OpenAIEmbedder calls the OpenAI embeddings API.
type OpenAIEmbedder struct {
	client  *http.Client
	apiKey  string
	model   string
	baseURL string
}

// NewOpenAIEmbedder creates an Embedder backed by the OpenAI embeddings API.
// cfg.Model defaults to text-embedding-3-small; cfg.BaseURL defaults to the OpenAI endpoint.
func NewOpenAIEmbedder(cfg EmbedderConfig) (*OpenAIEmbedder, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("openai embedder: APIKey is required")
	}
	model := cfg.Model
	if model == "" {
		model = openAIDefaultModel
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = openAIDefaultBaseURL
	}
	return &OpenAIEmbedder{
		client:  &http.Client{Timeout: 30 * time.Second},
		apiKey:  cfg.APIKey,
		model:   model,
		baseURL: baseURL,
	}, nil
}

// Model returns the embedding model name.
func (e *OpenAIEmbedder) Model() string { return e.model }

type openAIEmbedRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Embed returns the embedding vector for the given text.
func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(openAIEmbedRequest{Input: text, Model: e.model})
	if err != nil {
		return nil, fmt.Errorf("openai embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embed: http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai embed: read body: %w", err)
	}

	var result openAIEmbedResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("openai embed: unmarshal response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("openai embed: API error (%s): %s", result.Error.Type, result.Error.Message)
	}

	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("openai embed: empty embedding in response")
	}

	return result.Data[0].Embedding, nil
}
