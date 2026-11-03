package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/attest-ai/attest/engine/internal/llm"
)

// AnthropicDefaultBaseURL is the Anthropic API root; the provider appends
// /v1/messages.
const AnthropicDefaultBaseURL = "https://api.anthropic.com"

// anthropicVersion is the Messages API version header the request shape
// below is written against.
const anthropicVersion = "2023-06-01"

// Anthropic implements llm.Provider on the Anthropic Messages API.
type Anthropic struct {
	client  *http.Client
	apiKey  string
	model   string
	baseURL string
}

// NewAnthropic returns a Messages API provider. apiKey and model are
// required; an empty baseURL selects AnthropicDefaultBaseURL.
func NewAnthropic(apiKey, model, baseURL string) (*Anthropic, error) {
	if apiKey == "" {
		return nil, errors.New("anthropic provider: apiKey is required")
	}
	if model == "" {
		return nil, errors.New("anthropic provider: model is required (set ATTEST_JUDGE_MODEL)")
	}
	if baseURL == "" {
		baseURL = AnthropicDefaultBaseURL
	}
	return &Anthropic{
		client:  newHTTPClient(),
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
	}, nil
}

// Name returns "anthropic".
func (p *Anthropic) Name() string { return "anthropic" }

// DefaultModel returns the configured model.
func (p *Anthropic) DefaultModel() string { return p.model }

// MaxOutputTokens returns the output budget actually sent for a request
// asking for requested tokens (see outputBudget).
func (p *Anthropic) MaxOutputTokens(requested int) int { return outputBudget(requested) }

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicRequest omits sampling parameters: current Claude models reject
// a non-default temperature, so the model's default sampling is used.
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	// input_tokens excludes prompt-cache reads and writes, which the
	// Messages API reports separately.
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type anthropicError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func anthropicErrorMessage(raw []byte) string {
	var e anthropicError
	if json.Unmarshal(raw, &e) != nil || e.Error.Message == "" {
		return ""
	}
	return e.Error.Type + ": " + e.Error.Message
}

// Complete sends one Messages API request. Text content blocks are
// concatenated into the response content; thinking blocks are ignored.
func (p *Anthropic) Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	body := anthropicRequest{
		Model:     model,
		MaxTokens: outputBudget(req.MaxTokens),
		System:    req.SystemPrompt,
		Messages:  make([]anthropicMessage, 0, len(req.Messages)),
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, anthropicMessage{Role: m.Role, Content: m.Content})
	}

	start := time.Now()
	raw, err := postJSON(ctx, p.client, postRequest{
		provider: "anthropic",
		url:      p.baseURL + "/v1/messages",
		apiKey:   p.apiKey,
		headers: map[string]string{
			"x-api-key":         p.apiKey,
			"anthropic-version": anthropicVersion,
		},
		body:       body,
		errMessage: anthropicErrorMessage,
	})
	if err != nil {
		return nil, err
	}

	var resp anthropicResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("anthropic: unmarshal response: %w", err)
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if text.Len() == 0 {
		return nil, errors.New("anthropic: response has no text content")
	}
	respModel := resp.Model
	if respModel == "" {
		respModel = model
	}
	return &llm.CompletionResponse{
		Content: text.String(),
		Model:   respModel,
		InputTokens: resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens +
			resp.Usage.CacheCreationInputTokens,
		OutputTokens:      resp.Usage.OutputTokens,
		CachedInputTokens: resp.Usage.CacheReadInputTokens,
		DurationMS:        time.Since(start).Milliseconds(),
	}, nil
}
