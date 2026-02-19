package simulation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/llm"
)

// echoAgent returns the user message prefixed with "echo: ".
func echoAgent(_ context.Context, userMessage string) (string, error) {
	return "echo: " + userMessage, nil
}

// newUserMock builds a MockProvider that cycles through the given strings as responses.
func newUserMock(responses []string) *llm.MockProvider {
	var resps []*llm.CompletionResponse
	for _, r := range responses {
		resps = append(resps, &llm.CompletionResponse{Content: r, Model: "mock-model"})
	}
	return llm.NewMockProvider(resps, nil)
}

func TestOrchestratorMaxTurns(t *testing.T) {
	mock := newUserMock([]string{"follow-up 1", "follow-up 2", "follow-up 3"})
	cfg := SimulationConfig{
		Persona:  FriendlyUser,
		MaxTurns: 3,
		Provider: mock,
	}
	orch := NewOrchestrator(cfg)

	result, err := orch.RunSimulation(context.Background(), "hello", echoAgent)
	if err != nil {
		t.Fatalf("RunSimulation error: %v", err)
	}
	if result.TotalTurns != 3 {
		t.Errorf("TotalTurns = %d, want 3", result.TotalTurns)
	}
	if result.StoppedBy != "max_turns" {
		t.Errorf("StoppedBy = %q, want %q", result.StoppedBy, "max_turns")
	}
}

func TestOrchestratorKeywordStop(t *testing.T) {
	// Agent returns "goodbye" on the 2nd turn, which should trigger keyword stop.
	callCount := 0
	keywordAgent := func(_ context.Context, userMessage string) (string, error) {
		callCount++
		if callCount == 2 {
			return "goodbye and farewell", nil
		}
		return "still going", nil
	}

	mock := newUserMock([]string{"follow-up"})
	cfg := SimulationConfig{
		Persona:  FriendlyUser,
		MaxTurns: 10,
		StopConditions: []StopCondition{
			KeywordStopCondition{Keywords: []string{"goodbye", "done"}},
		},
		Provider: mock,
	}
	orch := NewOrchestrator(cfg)

	result, err := orch.RunSimulation(context.Background(), "start", keywordAgent)
	if err != nil {
		t.Fatalf("RunSimulation error: %v", err)
	}
	if result.TotalTurns != 2 {
		t.Errorf("TotalTurns = %d, want 2", result.TotalTurns)
	}
	if !strings.HasPrefix(result.StoppedBy, "keyword:") {
		t.Errorf("StoppedBy = %q, want prefix %q", result.StoppedBy, "keyword:")
	}
}

func TestOrchestratorMultipleTurns(t *testing.T) {
	userResponses := []string{"user turn 2", "user turn 3"}
	mock := newUserMock(userResponses)
	cfg := SimulationConfig{
		Persona:  FriendlyUser,
		MaxTurns: 3,
		Provider: mock,
	}
	orch := NewOrchestrator(cfg)

	result, err := orch.RunSimulation(context.Background(), "initial", echoAgent)
	if err != nil {
		t.Fatalf("RunSimulation error: %v", err)
	}

	if len(result.Turns) != 3 {
		t.Fatalf("len(Turns) = %d, want 3", len(result.Turns))
	}

	// Turn 1: user = "initial", agent = "echo: initial"
	if result.Turns[0].UserMessage != "initial" {
		t.Errorf("turn 1 UserMessage = %q, want %q", result.Turns[0].UserMessage, "initial")
	}
	if result.Turns[0].AgentResponse != "echo: initial" {
		t.Errorf("turn 1 AgentResponse = %q, want %q", result.Turns[0].AgentResponse, "echo: initial")
	}

	// Turn 2: user = "user turn 2" (from mock), agent = "echo: user turn 2"
	if result.Turns[1].UserMessage != "user turn 2" {
		t.Errorf("turn 2 UserMessage = %q, want %q", result.Turns[1].UserMessage, "user turn 2")
	}
	if result.Turns[1].AgentResponse != "echo: user turn 2" {
		t.Errorf("turn 2 AgentResponse = %q, want %q", result.Turns[1].AgentResponse, "echo: user turn 2")
	}

	// Turn 3: user = "user turn 3", agent = "echo: user turn 3"
	if result.Turns[2].UserMessage != "user turn 3" {
		t.Errorf("turn 3 UserMessage = %q, want %q", result.Turns[2].UserMessage, "user turn 3")
	}

	// Verify turn numbers are sequential.
	for i, turn := range result.Turns {
		if turn.TurnNumber != i+1 {
			t.Errorf("turn[%d].TurnNumber = %d, want %d", i, turn.TurnNumber, i+1)
		}
	}
}

func TestOrchestratorSingleTurn(t *testing.T) {
	mock := newUserMock(nil)
	cfg := SimulationConfig{
		Persona:  FriendlyUser,
		MaxTurns: 1,
		Provider: mock,
	}
	orch := NewOrchestrator(cfg)

	result, err := orch.RunSimulation(context.Background(), "one shot", echoAgent)
	if err != nil {
		t.Fatalf("RunSimulation error: %v", err)
	}
	if result.TotalTurns != 1 {
		t.Errorf("TotalTurns = %d, want 1", result.TotalTurns)
	}
	if len(result.Turns) != 1 {
		t.Fatalf("len(Turns) = %d, want 1", len(result.Turns))
	}
	if result.Turns[0].UserMessage != "one shot" {
		t.Errorf("UserMessage = %q, want %q", result.Turns[0].UserMessage, "one shot")
	}
	if result.StoppedBy != "max_turns" {
		t.Errorf("StoppedBy = %q, want %q", result.StoppedBy, "max_turns")
	}
	// No user generation should have occurred.
	if mock.GetCallCount() != 0 {
		t.Errorf("mock call count = %d, want 0 (no user generation for single turn)", mock.GetCallCount())
	}
}

func TestOrchestratorAgentError(t *testing.T) {
	mock := newUserMock(nil)
	cfg := SimulationConfig{
		Persona:  FriendlyUser,
		MaxTurns: 3,
		Provider: mock,
	}
	orch := NewOrchestrator(cfg)

	failAgent := func(_ context.Context, _ string) (string, error) {
		return "", fmt.Errorf("agent failure")
	}

	_, err := orch.RunSimulation(context.Background(), "start", failAgent)
	if err == nil {
		t.Fatal("expected error from failing agent, got nil")
	}
}
