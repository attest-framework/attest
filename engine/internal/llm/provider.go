package llm

import "context"

// Message is a single message in a conversation.
type Message struct {
	Role    string
	Content string
}

// CompletionRequest holds parameters for a completion call.
type CompletionRequest struct {
	Model        string
	SystemPrompt string
	Messages     []Message
	Temperature  float64
	MaxTokens    int
}

// CompletionResponse holds the result of a completion call.
type CompletionResponse struct {
	Content      string
	Model        string
	InputTokens  int
	OutputTokens int
	// CachedInputTokens is the part of InputTokens the provider served
	// from its prompt cache.
	CachedInputTokens int
	Cost              float64
	DurationMS        int64
}

// Provider is the interface that wraps an LLM backend.
type Provider interface {
	Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)
	Name() string
	DefaultModel() string
}

// OutputLimiter is implemented by providers that send a different output
// token limit than CompletionRequest.MaxTokens. The budget ledger reserves
// against the limit actually sent.
type OutputLimiter interface {
	MaxOutputTokens(requested int) int
}
