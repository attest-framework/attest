package llm

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRateLimiter_Concurrency(t *testing.T) {
	mock := NewMockProvider([]*CompletionResponse{
		{
			Content:      `{"score": 0.5, "explanation": "ok"}`,
			Model:        "mock-model",
			InputTokens:  10,
			OutputTokens: 10,
			Cost:         0.001,
			DurationMS:   10,
		},
	}, nil)

	cfg := RateLimiterConfig{
		RequestsPerMinute: 600, // 10/sec
		Burst:             10,
		MaxRetries:        0,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        1 * time.Second,
	}

	rl, err := NewRateLimitedProvider(mock, cfg)
	if err != nil {
		t.Fatalf("NewRateLimitedProvider: %v", err)
	}

	const numRequests = 50
	var wg sync.WaitGroup
	errs := make(chan error, numRequests)

	start := time.Now()
	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := &CompletionRequest{
				Model:        "mock-model",
				SystemPrompt: "test",
				Messages:     []Message{{Role: "user", Content: "hello"}},
			}
			_, err := rl.Complete(context.Background(), req)
			if err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)
	elapsed := time.Since(start)

	// Collect errors
	var failures []error
	for e := range errs {
		failures = append(failures, e)
	}
	if len(failures) > 0 {
		t.Errorf("expected 0 errors, got %d; first: %v", len(failures), failures[0])
	}

	// 50 requests at 10/sec with burst 10: first 10 are instant,
	// remaining 40 at 10/sec = 4s. Use 3s as conservative lower bound.
	if elapsed < 3*time.Second {
		t.Errorf("expected wall-clock >= 3s (proves rate limiting), got %v", elapsed)
	}

	callCount := mock.GetCallCount()
	if callCount != numRequests {
		t.Errorf("expected %d calls to mock, got %d", numRequests, callCount)
	}
}

func TestRateLimiter_RetryOnError(t *testing.T) {
	successResp := &CompletionResponse{
		Content:      `{"score": 0.8, "explanation": "good"}`,
		Model:        "mock-model",
		InputTokens:  10,
		OutputTokens: 10,
		Cost:         0.001,
		DurationMS:   10,
	}

	// First 2 calls fail, 3rd succeeds
	mock := NewMockProvider(
		[]*CompletionResponse{successResp},
		[]error{
			fmt.Errorf("transient error 1"),
			fmt.Errorf("transient error 2"),
			nil, // 3rd call succeeds — falls through to Responses
		},
	)

	cfg := RateLimiterConfig{
		RequestsPerMinute: 600,
		Burst:             10,
		MaxRetries:        3,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
	}

	rl, err := NewRateLimitedProvider(mock, cfg)
	if err != nil {
		t.Fatalf("NewRateLimitedProvider: %v", err)
	}

	req := &CompletionRequest{
		Model:    "mock-model",
		Messages: []Message{{Role: "user", Content: "test"}},
	}

	resp, err := rl.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}

	if resp.Content != successResp.Content {
		t.Errorf("unexpected response content: %s", resp.Content)
	}

	callCount := mock.GetCallCount()
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", callCount)
	}
}
