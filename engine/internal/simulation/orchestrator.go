package simulation

import (
	"context"
	"fmt"
	"strings"

	"github.com/attest-ai/attest/engine/internal/llm"
)

// StopCondition determines whether a simulation should terminate.
type StopCondition interface {
	ShouldStop(turn int, lastResponse string) bool
}

// MaxTurnsCondition stops the simulation after a fixed number of turns.
type MaxTurnsCondition struct {
	MaxTurns int
}

func (c MaxTurnsCondition) ShouldStop(turn int, _ string) bool {
	return turn >= c.MaxTurns
}

// KeywordStopCondition stops the simulation when any keyword appears in the agent response.
type KeywordStopCondition struct {
	Keywords []string
}

func (c KeywordStopCondition) ShouldStop(_ int, lastResponse string) bool {
	lower := strings.ToLower(lastResponse)
	for _, kw := range c.Keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// matchedKeyword returns the first keyword found in response, or empty string.
func (c KeywordStopCondition) matchedKeyword(response string) string {
	lower := strings.ToLower(response)
	for _, kw := range c.Keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return kw
		}
	}
	return ""
}

// SimulationConfig holds all parameters needed to run a simulation.
type SimulationConfig struct {
	Persona        Persona
	MaxTurns       int
	StopConditions []StopCondition
	Provider       llm.Provider
}

// Turn represents one exchange in a simulation.
type Turn struct {
	TurnNumber    int
	UserMessage   string
	AgentResponse string
}

// SimulationResult holds the complete record of a finished simulation.
type SimulationResult struct {
	Turns      []Turn
	TotalTurns int
	StoppedBy  string
}

// Orchestrator runs a multi-turn simulation between a SimulatedUser and an agent callback.
type Orchestrator struct {
	config SimulationConfig
	user   *SimulatedUser
}

// NewOrchestrator creates an Orchestrator from the given config.
func NewOrchestrator(config SimulationConfig) *Orchestrator {
	return &Orchestrator{
		config: config,
		user:   NewSimulatedUser(config.Persona, config.Provider),
	}
}

// RunSimulation executes the simulation by alternating between the SimulatedUser and agentFn.
//
// Turn 1: initialPrompt → agentFn → agentResponse
// Turn N: SimulatedUser.GenerateMessage(history) → userMessage → agentFn → agentResponse
//
// Stops when MaxTurns is reached or any StopCondition fires.
func (o *Orchestrator) RunSimulation(
	ctx context.Context,
	initialPrompt string,
	agentFn func(ctx context.Context, userMessage string) (string, error),
) (*SimulationResult, error) {
	result := &SimulationResult{}

	// conversationHistory tracks the full exchange for the SimulatedUser's context.
	var conversationHistory []llm.Message

	currentUserMessage := initialPrompt
	maxTurnsCondition := MaxTurnsCondition{MaxTurns: o.config.MaxTurns}

	for turn := 1; ; turn++ {
		// Call the agent with the current user message.
		agentResponse, err := agentFn(ctx, currentUserMessage)
		if err != nil {
			return nil, fmt.Errorf("simulation turn %d: agent error: %w", turn, err)
		}

		result.Turns = append(result.Turns, Turn{
			TurnNumber:    turn,
			UserMessage:   currentUserMessage,
			AgentResponse: agentResponse,
		})
		result.TotalTurns = turn

		// Update conversation history with this exchange.
		conversationHistory = append(conversationHistory,
			llm.Message{Role: "user", Content: currentUserMessage},
			llm.Message{Role: "assistant", Content: agentResponse},
		)

		// Check max turns first.
		if maxTurnsCondition.ShouldStop(turn, agentResponse) {
			result.StoppedBy = "max_turns"
			break
		}

		// Check custom stop conditions.
		stopped := false
		for _, cond := range o.config.StopConditions {
			if cond.ShouldStop(turn, agentResponse) {
				switch c := cond.(type) {
				case KeywordStopCondition:
					kw := c.matchedKeyword(agentResponse)
					result.StoppedBy = fmt.Sprintf("keyword:%s", kw)
				default:
					result.StoppedBy = "condition"
				}
				stopped = true
				break
			}
		}
		if stopped {
			break
		}

		// Generate the next user message.
		nextUserMessage, err := o.user.GenerateMessage(ctx, conversationHistory)
		if err != nil {
			return nil, fmt.Errorf("simulation turn %d: user generation error: %w", turn, err)
		}
		currentUserMessage = nextUserMessage
	}

	return result, nil
}
