package llm

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
	openAIDefaultModel   = "gpt-4.1"
	OpenAIDefaultBaseURL = "https://api.openai.com/v1"
)

// OpenAIProvider implements Provider using the OpenAI chat completions API.
type OpenAIProvider struct {
	client  *http.Client
	apiKey  string
	model   string
	baseURL string
}

// NewOpenAIProvider creates a Provider backed by the OpenAI chat completions API.
func NewOpenAIProvider(apiKey, model, baseURL string) (*OpenAIProvider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("openai provider: apiKey is required")
	}
	if model == "" {
		model = openAIDefaultModel
	}
	if baseURL == "" {
		baseURL = OpenAIDefaultBaseURL
	}
	return &OpenAIProvider{
		// Redirects are not followed, so a redirecting endpoint cannot
		// receive a replay of the judge prompt.
		client: &http.Client{
			Timeout: 60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
	}, nil
}

// Name returns the provider name.
func (p *OpenAIProvider) Name() string { return "openai" }

// DefaultModel returns the default model for this provider.
func (p *OpenAIProvider) DefaultModel() string { return p.model }

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model       string              `json:"model"`
	Messages    []openAIChatMessage `json:"messages"`
	Temperature float64             `json:"temperature,omitempty"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message openAIChatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Complete sends a chat completion request and returns the response.
func (p *OpenAIProvider) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	messages := make([]openAIChatMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, openAIChatMessage{Role: "system", Content: req.SystemPrompt})
	}
	for _, m := range req.Messages {
		messages = append(messages, openAIChatMessage{Role: m.Role, Content: m.Content})
	}

	chatReq := openAIChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("openai complete: marshal: %w", err)
	}

	start := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai complete: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai complete: http: %w", err)
	}
	defer httpResp.Body.Close()
	durationMS := time.Since(start).Milliseconds()

	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai complete: read body: %w", err)
	}

	var chatResp openAIChatResponse
	if err := json.Unmarshal(raw, &chatResp); err != nil {
		return nil, fmt.Errorf("openai complete: unmarshal: %w", err)
	}

	if chatResp.Error != nil {
		return nil, fmt.Errorf("openai complete: API error (%s): %s", chatResp.Error.Type, chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("openai complete: no choices in response")
	}

	inputTokens := chatResp.Usage.PromptTokens
	outputTokens := chatResp.Usage.CompletionTokens
	cost := estimateOpenAICost(model, inputTokens, outputTokens)

	return &CompletionResponse{
		Content:      chatResp.Choices[0].Message.Content,
		Model:        chatResp.Model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Cost:         cost,
		DurationMS:   durationMS,
	}, nil
}

// estimateOpenAICost returns a rough USD cost estimate based on public pricing.
// Prices are per million tokens.
func estimateOpenAICost(model string, inputTokens, outputTokens int) float64 {
	var inputPricePer1M, outputPricePer1M float64
	switch model {
	case "gpt-4.1":
		inputPricePer1M = 2.00
		outputPricePer1M = 8.00
	case "gpt-4.1-mini":
		inputPricePer1M = 0.40
		outputPricePer1M = 1.60
	default:
		// Unknown model — return 0
		return 0
	}
	return (float64(inputTokens)*inputPricePer1M + float64(outputTokens)*outputPricePer1M) / 1_000_000
}
