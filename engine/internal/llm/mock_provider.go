package llm

import (
	"context"
	"sync"
)

// MockProvider implements Provider with configurable responses for testing.
type MockProvider struct {
	mu          sync.Mutex
	Responses   []*CompletionResponse
	Errors      []error
	CallCount   int
	LastRequest *CompletionRequest
}

// NewMockProvider creates a MockProvider cycling through the given responses.
// If both are nil/empty, returns a default successful response.
func NewMockProvider(responses []*CompletionResponse, errors []error) *MockProvider {
	return &MockProvider{Responses: responses, Errors: errors}
}

func (m *MockProvider) Name() string        { return "mock" }
func (m *MockProvider) DefaultModel() string { return "mock-model" }

func (m *MockProvider) Complete(_ context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.CallCount
	m.CallCount++
	m.LastRequest = req

	// Return error if configured for this call index
	if idx < len(m.Errors) && m.Errors[idx] != nil {
		return nil, m.Errors[idx]
	}

	// Return configured response (cycle if needed)
	if len(m.Responses) > 0 {
		return m.Responses[idx%len(m.Responses)], nil
	}

	// Default response
	return &CompletionResponse{
		Content:      `{"score": 0.5, "explanation": "default mock response"}`,
		Model:        "mock-model",
		InputTokens:  10,
		OutputTokens: 10,
		Cost:         0.001,
		DurationMS:   50,
	}, nil
}

// GetCallCount returns the number of times Complete has been called.
func (m *MockProvider) GetCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.CallCount
}
