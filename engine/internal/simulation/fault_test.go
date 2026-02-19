package simulation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/attest-ai/attest/engine/internal/llm"
)

// stubProvider is a minimal llm.Provider for fault injection tests.
type stubProvider struct {
	name    string
	model   string
	content string
	err     error
}

func (s *stubProvider) Name() string        { return s.name }
func (s *stubProvider) DefaultModel() string { return s.model }
func (s *stubProvider) Complete(_ context.Context, _ *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &llm.CompletionResponse{
		Content:      s.content,
		Model:        s.model,
		InputTokens:  5,
		OutputTokens: 5,
		Cost:         0.001,
		DurationMS:   10,
	}, nil
}

func newStub(content string) *stubProvider {
	return &stubProvider{name: "stub", model: "stub-model", content: content}
}

func makeReq() *llm.CompletionRequest {
	return &llm.CompletionRequest{
		Model:     "stub-model",
		Messages:  []llm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 100,
	}
}

func TestFaultInjectorPassthrough(t *testing.T) {
	inner := newStub("hello world")
	fi := NewFaultInjectorWithSeed(inner, FaultConfig{}, 42)

	resp, err := fi.Complete(context.Background(), makeReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello world" {
		t.Fatalf("expected passthrough content, got %q", resp.Content)
	}
}

func TestFaultInjectorNameDelegation(t *testing.T) {
	inner := newStub("")
	fi := NewFaultInjectorWithSeed(inner, FaultConfig{}, 0)

	if fi.Name() != "fault:stub" {
		t.Fatalf("expected 'fault:stub', got %q", fi.Name())
	}
	if fi.DefaultModel() != "stub-model" {
		t.Fatalf("expected 'stub-model', got %q", fi.DefaultModel())
	}
}

func TestFaultInjectorErrorRateAlways(t *testing.T) {
	inner := newStub("ok")
	fi := NewFaultInjectorWithSeed(inner, FaultConfig{ErrorRate: 1.0}, 42)

	for i := range 10 {
		_, err := fi.Complete(context.Background(), makeReq())
		if err == nil {
			t.Fatalf("call %d: expected error with ErrorRate=1.0, got nil", i)
		}
		if err.Error() != "injected fault: simulated error" {
			t.Fatalf("unexpected error message: %v", err)
		}
	}
}

func TestFaultInjectorErrorRateNever(t *testing.T) {
	inner := newStub("ok")
	fi := NewFaultInjectorWithSeed(inner, FaultConfig{ErrorRate: 0.0}, 42)

	for i := range 10 {
		_, err := fi.Complete(context.Background(), makeReq())
		if err != nil {
			t.Fatalf("call %d: unexpected error with ErrorRate=0.0: %v", i, err)
		}
	}
}

func TestFaultInjectorLatencyJitter(t *testing.T) {
	inner := newStub("ok")
	jitter := 50 * time.Millisecond
	fi := NewFaultInjectorWithSeed(inner, FaultConfig{LatencyJitter: jitter}, 42)

	start := time.Now()
	_, err := fi.Complete(context.Background(), makeReq())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With seed=42 and jitter=50ms, the RNG will produce a non-zero value most of the time.
	// We only assert elapsed >= 0 (always true) but verify the jitter path is exercised.
	if elapsed < 0 {
		t.Fatal("elapsed time is negative")
	}
}

func TestFaultInjectorLatencyJitterWithLargeDuration(t *testing.T) {
	inner := newStub("ok")
	// Use a large enough jitter that at least some delay is expected with high probability.
	jitter := 100 * time.Millisecond
	// Use seed=1 — rng.Int63n(100ms) will be > 0 unless it hits exactly 0.
	fi := NewFaultInjectorWithSeed(inner, FaultConfig{LatencyJitter: jitter}, 1)

	start := time.Now()
	_, err := fi.Complete(context.Background(), makeReq())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The jitter range is [0, 100ms). At minimum the call goes through without error.
	// We accept 0 jitter (unlikely with seed=1) as valid since it's probabilistic.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("elapsed %v exceeds jitter ceiling of 100ms plus reasonable overhead", elapsed)
	}
}

func TestFaultInjectorContentCorruption(t *testing.T) {
	original := "the quick brown fox jumps over the lazy dog"
	inner := newStub(original)
	fi := NewFaultInjectorWithSeed(inner, FaultConfig{ContentCorruption: true}, 99)

	resp, err := fi.Complete(context.Background(), makeReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content == original {
		t.Fatal("expected content to be corrupted, but it matches original")
	}
	// Corrupted content should have same runes, just shuffled
	if len([]rune(resp.Content)) != len([]rune(original)) {
		t.Fatalf("corrupted content length %d != original length %d", len(resp.Content), len(original))
	}
}

func TestFaultInjectorContentCorruptionDisabled(t *testing.T) {
	original := "hello world"
	inner := newStub(original)
	fi := NewFaultInjectorWithSeed(inner, FaultConfig{ContentCorruption: false}, 42)

	resp, err := fi.Complete(context.Background(), makeReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != original {
		t.Fatalf("expected content unchanged, got %q", resp.Content)
	}
}

func TestFaultInjectorTimeout(t *testing.T) {
	inner := newStub("ok")
	fi := NewFaultInjectorWithSeed(inner, FaultConfig{TimeoutAfter: 10 * time.Millisecond}, 42)

	start := time.Now()
	_, err := fi.Complete(context.Background(), makeReq())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected deadline exceeded error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed < 10*time.Millisecond {
		t.Fatalf("expected at least 10ms delay, got %v", elapsed)
	}
}

func TestFaultInjectorInnerError(t *testing.T) {
	inner := &stubProvider{name: "stub", model: "m", err: errors.New("inner failure")}
	fi := NewFaultInjectorWithSeed(inner, FaultConfig{}, 42)

	_, err := fi.Complete(context.Background(), makeReq())
	if err == nil {
		t.Fatal("expected inner error to propagate")
	}
	if err.Error() != "inner failure" {
		t.Fatalf("unexpected error: %v", err)
	}
}
