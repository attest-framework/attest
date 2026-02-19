package llm

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MockProvider implements Provider with configurable responses for testing.
type MockProvider struct {
	mu               sync.Mutex
	Responses        []*CompletionResponse
	Errors           []error
	CallCount        int
	LastRequest      *CompletionRequest
	RequestHistory   []CompletionRequest
	ReplayMode       bool
	SimulatedLatency time.Duration
	MatchFunc        func(*CompletionRequest) *CompletionResponse
}

// NewMockProvider creates a MockProvider cycling through the given responses.
// If both are nil/empty, returns a default successful response.
func NewMockProvider(responses []*CompletionResponse, errors []error) *MockProvider {
	return &MockProvider{Responses: responses, Errors: errors}
}

// NewReplayProvider creates a MockProvider that uses responses exactly once in order.
// Returns an error when all responses have been consumed.
func NewReplayProvider(responses []*CompletionResponse) *MockProvider {
	return &MockProvider{Responses: responses, ReplayMode: true}
}

func (m *MockProvider) Name() string        { return "mock" }
func (m *MockProvider) DefaultModel() string { return "mock-model" }

func (m *MockProvider) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	m.mu.Lock()
	latency := m.SimulatedLatency
	m.mu.Unlock()

	if latency > 0 {
		select {
		case <-time.After(latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.CallCount
	m.CallCount++
	m.LastRequest = req
	m.RequestHistory = append(m.RequestHistory, *req)

	// Return error if configured for this call index
	if idx < len(m.Errors) && m.Errors[idx] != nil {
		return nil, m.Errors[idx]
	}

	// MatchFunc takes priority over index-based selection
	if m.MatchFunc != nil {
		if resp := m.MatchFunc(req); resp != nil {
			return resp, nil
		}
	}

	// ReplayMode: consume responses exactly once
	if m.ReplayMode {
		if idx >= len(m.Responses) {
			return nil, fmt.Errorf("mock provider: all %d responses exhausted at call %d", len(m.Responses), idx)
		}
		return m.Responses[idx], nil
	}

	// Default cycling behavior
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

// GetRequestHistory returns a copy of all requests made to this provider.
func (m *MockProvider) GetRequestHistory() []CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]CompletionRequest(nil), m.RequestHistory...)
}
