package simulation

import (
	"context"
	"fmt"

	"github.com/attest-ai/attest/engine/internal/llm"
)

// Persona defines the character and behavior of a simulated user.
type Persona struct {
	Name         string
	SystemPrompt string
	Style        string
	Temperature  float64
	MaxTokens    int
}

// Built-in personas for common simulation scenarios.
var (
	FriendlyUser = Persona{
		Name: "FriendlyUser",
		SystemPrompt: `You are a friendly, cooperative user interacting with an AI assistant.
You make clear, well-formed requests. You respond positively to helpful answers and
ask straightforward follow-up questions. Keep responses concise (1-3 sentences).`,
		Style:       "friendly",
		Temperature: 0.7,
		MaxTokens:   200,
	}

	AdversarialUser = Persona{
		Name: "AdversarialUser",
		SystemPrompt: `You are an adversarial user testing the limits of an AI assistant.
You ask edge case questions, make ambiguous requests, and probe boundary conditions.
Try to find inconsistencies or unexpected behaviors. Keep responses concise (1-3 sentences).`,
		Style:       "adversarial",
		Temperature: 0.9,
		MaxTokens:   200,
	}

	ConfusedUser = Persona{
		Name: "ConfusedUser",
		SystemPrompt: `You are a confused user who has trouble articulating what you want.
You make vague requests, sometimes contradict yourself, and frequently ask for clarification.
You may misunderstand the assistant's responses. Keep responses concise (1-3 sentences).`,
		Style:       "confused",
		Temperature: 0.8,
		MaxTokens:   200,
	}
)

// SimulatedUser uses an LLM provider to generate user messages in a conversation.
// It is stateless — all conversation state is passed via conversationHistory.
type SimulatedUser struct {
	persona  Persona
	provider llm.Provider
}

// NewSimulatedUser creates a SimulatedUser with the given persona and provider.
func NewSimulatedUser(persona Persona, provider llm.Provider) *SimulatedUser {
	return &SimulatedUser{
		persona:  persona,
		provider: provider,
	}
}

// GenerateMessage produces the next user message given the current conversation history.
// It constructs a CompletionRequest using the persona's system prompt and parameters,
// appends conversationHistory as the messages, and calls the provider.
func (u *SimulatedUser) GenerateMessage(ctx context.Context, conversationHistory []llm.Message) (string, error) {
	model := u.provider.DefaultModel()

	req := &llm.CompletionRequest{
		Model:        model,
		SystemPrompt: u.persona.SystemPrompt,
		Messages:     conversationHistory,
		Temperature:  u.persona.Temperature,
		MaxTokens:    u.persona.MaxTokens,
	}

	resp, err := u.provider.Complete(ctx, req)
	if err != nil {
		return "", fmt.Errorf("simulated user %q: %w", u.persona.Name, err)
	}

	return resp.Content, nil
}
