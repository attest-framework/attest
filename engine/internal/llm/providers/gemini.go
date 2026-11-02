package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/attest-ai/attest/engine/internal/llm"
)

// GeminiDefaultBaseURL is the Gemini API root; the provider appends
// /v1beta/models/<model>:generateContent.
const GeminiDefaultBaseURL = "https://generativelanguage.googleapis.com"

// Gemini implements llm.Provider on the Gemini generateContent API.
type Gemini struct {
	client  *http.Client
	apiKey  string
	model   string
	baseURL string
}

// NewGemini returns a generateContent provider. apiKey and model are
// required; an empty baseURL selects GeminiDefaultBaseURL.
func NewGemini(apiKey, model, baseURL string) (*Gemini, error) {
	if apiKey == "" {
		return nil, errors.New("gemini provider: apiKey is required")
	}
	if model == "" {
		return nil, errors.New("gemini provider: model is required (set ATTEST_JUDGE_MODEL)")
	}
	if baseURL == "" {
		baseURL = GeminiDefaultBaseURL
	}
	return &Gemini{
		client:  newHTTPClient(),
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
	}, nil
}

// Name returns "gemini".
func (p *Gemini) Name() string { return "gemini" }

// DefaultModel returns the configured model.
func (p *Gemini) DefaultModel() string { return p.model }

// MaxOutputTokens returns the output budget actually sent for a request
// asking for requested tokens (see outputBudget).
func (p *Gemini) MaxOutputTokens(requested int) int { return outputBudget(requested) }

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	Temperature     float64 `json:"temperature"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	Contents          []geminiContent        `json:"contents"`
	GenerationConfig  geminiGenerationConfig `json:"generationConfig"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		// CachedContentTokenCount is included in PromptTokenCount.
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		// ThoughtsTokenCount is billed at the output price but not
		// included in CandidatesTokenCount.
		ThoughtsTokenCount int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
}

type geminiError struct {
	Error struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

func geminiErrorMessage(raw []byte) string {
	var e geminiError
	if json.Unmarshal(raw, &e) != nil || e.Error.Message == "" {
		return ""
	}
	return e.Error.Status + ": " + e.Error.Message
}

// geminiRole maps the chat role names used by llm.Message onto Gemini's
// "user" and "model" roles.
func geminiRole(role string) string {
	if role == "assistant" {
		return "model"
	}
	return "user"
}

// Complete sends one generateContent request and returns the text of the
// first candidate. The key travels in the x-goog-api-key header, never in
// the URL, so it cannot appear in logged request URLs.
func (p *Gemini) Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	body := geminiRequest{
		Contents:         make([]geminiContent, 0, len(req.Messages)),
		GenerationConfig: geminiGenerationConfig{Temperature: req.Temperature, MaxOutputTokens: outputBudget(req.MaxTokens)},
	}
	if req.SystemPrompt != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: req.SystemPrompt}}}
	}
	for _, m := range req.Messages {
		body.Contents = append(body.Contents, geminiContent{Role: geminiRole(m.Role), Parts: []geminiPart{{Text: m.Content}}})
	}

	endpoint := p.baseURL + "/v1beta/models/" + url.PathEscape(model) + ":generateContent"
	start := time.Now()
	raw, err := postJSON(ctx, p.client, postRequest{
		provider:   "gemini",
		url:        endpoint,
		apiKey:     p.apiKey,
		headers:    map[string]string{"x-goog-api-key": p.apiKey},
		body:       body,
		errMessage: geminiErrorMessage,
	})
	if err != nil {
		return nil, err
	}

	var resp geminiResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("gemini: unmarshal response: %w", err)
	}
	if len(resp.Candidates) == 0 {
		return nil, errors.New("gemini: response has no candidates")
	}
	var text strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		text.WriteString(part.Text)
	}
	if text.Len() == 0 {
		reason := truncate(stripControl(resp.Candidates[0].FinishReason), maxErrorDetail)
		return nil, fmt.Errorf("gemini: first candidate has no text (finishReason %q)", reason)
	}
	respModel := resp.ModelVersion
	if respModel == "" {
		respModel = model
	}
	return &llm.CompletionResponse{
		Content:           text.String(),
		Model:             respModel,
		InputTokens:       resp.UsageMetadata.PromptTokenCount,
		OutputTokens:      resp.UsageMetadata.CandidatesTokenCount + resp.UsageMetadata.ThoughtsTokenCount,
		CachedInputTokens: resp.UsageMetadata.CachedContentTokenCount,
		DurationMS:        time.Since(start).Milliseconds(),
	}, nil
}
