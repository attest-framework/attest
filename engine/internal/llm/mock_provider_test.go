package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMockProviderBackwardCompat(t *testing.T) {
	responses := []*CompletionResponse{
		{Content: "resp-0", Model: "mock-model"},
		{Content: "resp-1", Model: "mock-model"},
	}
	p := NewMockProvider(responses, nil)

	ctx := context.Background()

	// First two calls return resp-0 and resp-1
	r0, err := p.Complete(ctx, &CompletionRequest{Model: "mock-model"})
	if err != nil {
		t.Fatalf("call 0: unexpected error: %v", err)
	}
	if r0.Content != "resp-0" {
		t.Errorf("call 0: got content %q, want %q", r0.Content, "resp-0")
	}

	r1, err := p.Complete(ctx, &CompletionRequest{Model: "mock-model"})
	if err != nil {
		t.Fatalf("call 1: unexpected error: %v", err)
	}
	if r1.Content != "resp-1" {
		t.Errorf("call 1: got content %q, want %q", r1.Content, "resp-1")
	}

	// Third call cycles back to resp-0
	r2, err := p.Complete(ctx, &CompletionRequest{Model: "mock-model"})
	if err != nil {
		t.Fatalf("call 2: unexpected error: %v", err)
	}
	if r2.Content != "resp-0" {
		t.Errorf("call 2 (cycling): got content %q, want %q", r2.Content, "resp-0")
	}

	if p.GetCallCount() != 3 {
		t.Errorf("call count: got %d, want 3", p.GetCallCount())
	}
}

func TestMockProviderReplayMode(t *testing.T) {
	responses := []*CompletionResponse{
		{Content: "first", Model: "mock-model"},
		{Content: "second", Model: "mock-model"},
	}
	p := NewReplayProvider(responses)

	ctx := context.Background()

	r0, err := p.Complete(ctx, &CompletionRequest{})
	if err != nil {
		t.Fatalf("call 0: unexpected error: %v", err)
	}
	if r0.Content != "first" {
		t.Errorf("call 0: got %q, want %q", r0.Content, "first")
	}

	r1, err := p.Complete(ctx, &CompletionRequest{})
	if err != nil {
		t.Fatalf("call 1: unexpected error: %v", err)
	}
	if r1.Content != "second" {
		t.Errorf("call 1: got %q, want %q", r1.Content, "second")
	}

	// Third call exceeds responses — must return error
	_, err = p.Complete(ctx, &CompletionRequest{})
	if err == nil {
		t.Fatal("call 2: expected exhaustion error, got nil")
	}
}

func TestMockProviderRequestHistory(t *testing.T) {
	p := NewMockProvider(nil, nil)
	ctx := context.Background()

	req0 := &CompletionRequest{Model: "model-a", SystemPrompt: "sys-0"}
	req1 := &CompletionRequest{Model: "model-b", SystemPrompt: "sys-1"}

	if _, err := p.Complete(ctx, req0); err != nil {
		t.Fatalf("call 0: %v", err)
	}
	if _, err := p.Complete(ctx, req1); err != nil {
		t.Fatalf("call 1: %v", err)
	}

	history := p.GetRequestHistory()
	if len(history) != 2 {
		t.Fatalf("history length: got %d, want 2", len(history))
	}
	if history[0].Model != "model-a" || history[0].SystemPrompt != "sys-0" {
		t.Errorf("history[0]: got %+v", history[0])
	}
	if history[1].Model != "model-b" || history[1].SystemPrompt != "sys-1" {
		t.Errorf("history[1]: got %+v", history[1])
	}

	// Verify it's a copy — mutation does not affect internal state
	history[0].Model = "mutated"
	fresh := p.GetRequestHistory()
	if fresh[0].Model != "model-a" {
		t.Errorf("GetRequestHistory returned reference, not copy")
	}
}

func TestMockProviderMatchFunc(t *testing.T) {
	defaultResp := &CompletionResponse{Content: "default", Model: "mock-model"}
	matchedResp := &CompletionResponse{Content: "matched", Model: "mock-model"}

	p := NewMockProvider([]*CompletionResponse{defaultResp}, nil)
	p.MatchFunc = func(req *CompletionRequest) *CompletionResponse {
		if req.SystemPrompt == "trigger" {
			return matchedResp
		}
		return nil
	}

	ctx := context.Background()

	// Non-matching request falls through to index-based response
	r0, err := p.Complete(ctx, &CompletionRequest{SystemPrompt: "other"})
	if err != nil {
		t.Fatalf("call 0: %v", err)
	}
	if r0.Content != "default" {
		t.Errorf("call 0: got %q, want %q", r0.Content, "default")
	}

	// Matching request returns MatchFunc result
	r1, err := p.Complete(ctx, &CompletionRequest{SystemPrompt: "trigger"})
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if r1.Content != "matched" {
		t.Errorf("call 1: got %q, want %q", r1.Content, "matched")
	}
}

func TestMockProviderMatchFuncErrorPrecedence(t *testing.T) {
	matchedResp := &CompletionResponse{Content: "matched", Model: "mock-model"}
	expectedErr := errors.New("injected error")

	p := NewMockProvider(nil, []error{expectedErr})
	p.MatchFunc = func(_ *CompletionRequest) *CompletionResponse {
		return matchedResp
	}

	// Errors are checked before MatchFunc
	_, err := p.Complete(context.Background(), &CompletionRequest{})
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected injected error, got %v", err)
	}
}

func TestMockProviderSimulatedLatency(t *testing.T) {
	latency := 50 * time.Millisecond
	p := NewMockProvider(nil, nil)
	p.SimulatedLatency = latency

	ctx := context.Background()
	start := time.Now()
	if _, err := p.Complete(ctx, &CompletionRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < latency {
		t.Errorf("elapsed %v < simulated latency %v", elapsed, latency)
	}
}

func TestMockProviderSimulatedLatencyContextCancel(t *testing.T) {
	p := NewMockProvider(nil, nil)
	p.SimulatedLatency = 5 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := p.Complete(ctx, &CompletionRequest{})
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
}
