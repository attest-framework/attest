package simulation

import (
	"context"
	"testing"

	"github.com/attest-ai/attest/engine/internal/llm"
)

func TestSimulatedUserGenerateMessage(t *testing.T) {
	expectedContent := "What can you help me with?"
	mock := llm.NewMockProvider([]*llm.CompletionResponse{
		{Content: expectedContent, Model: "mock-model"},
	}, nil)

	user := NewSimulatedUser(FriendlyUser, mock)

	history := []llm.Message{
		{Role: "assistant", Content: "Hello! How can I help you today?"},
	}

	content, err := user.GenerateMessage(context.Background(), history)
	if err != nil {
		t.Fatalf("GenerateMessage returned error: %v", err)
	}
	if content != expectedContent {
		t.Errorf("content = %q, want %q", content, expectedContent)
	}

	// Verify the request was constructed correctly.
	reqs := mock.GetRequestHistory()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	req := reqs[0]

	if req.SystemPrompt != FriendlyUser.SystemPrompt {
		t.Errorf("system prompt mismatch: got %q, want %q", req.SystemPrompt, FriendlyUser.SystemPrompt)
	}
	if req.Temperature != FriendlyUser.Temperature {
		t.Errorf("temperature = %v, want %v", req.Temperature, FriendlyUser.Temperature)
	}
	if req.MaxTokens != FriendlyUser.MaxTokens {
		t.Errorf("max_tokens = %d, want %d", req.MaxTokens, FriendlyUser.MaxTokens)
	}
	if len(req.Messages) != len(history) {
		t.Errorf("messages count = %d, want %d", len(req.Messages), len(history))
	}
	if req.Messages[0].Content != history[0].Content {
		t.Errorf("message content = %q, want %q", req.Messages[0].Content, history[0].Content)
	}
}

func TestSimulatedUserWithPersonas(t *testing.T) {
	personas := []Persona{FriendlyUser, AdversarialUser, ConfusedUser}

	for _, persona := range personas {
		t.Run(persona.Name, func(t *testing.T) {
			mock := llm.NewMockProvider([]*llm.CompletionResponse{
				{Content: "test response", Model: "mock-model"},
			}, nil)

			user := NewSimulatedUser(persona, mock)

			_, err := user.GenerateMessage(context.Background(), nil)
			if err != nil {
				t.Fatalf("persona %q: GenerateMessage returned error: %v", persona.Name, err)
			}

			reqs := mock.GetRequestHistory()
			if len(reqs) != 1 {
				t.Fatalf("persona %q: expected 1 request, got %d", persona.Name, len(reqs))
			}
			req := reqs[0]

			if req.SystemPrompt == "" {
				t.Errorf("persona %q: system prompt is empty", persona.Name)
			}
			if req.Temperature <= 0 {
				t.Errorf("persona %q: temperature = %v, want > 0", persona.Name, req.Temperature)
			}
			if req.MaxTokens <= 0 {
				t.Errorf("persona %q: max_tokens = %d, want > 0", persona.Name, req.MaxTokens)
			}
		})
	}
}
