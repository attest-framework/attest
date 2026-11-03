package mcp

import "github.com/attest-ai/attest/engine/pkg/types"

// guideStep is where to send an agent for one kind of finding: a topic of
// `attest guide` and a one-line next step.
type guideStep struct {
	guide string
	next  string
}

// Findings are matched top to bottom; the first rule that fits wins.
type guideRule struct {
	reason string // matches when non-empty
	typ    string // matches when non-empty
	status string // matches when non-empty
	guideStep
}

var guideRules = []guideRule{
	{reason: types.ReasonReplayMiss, guideStep: guideStep{"replay", "A model request matched no recording; if the prompt or tools changed on purpose, re-record with `attest capture`, otherwise restore the request."}},
	{reason: types.ReasonMissingEvidence, typ: types.TypeEffect, guideStep: guideStep{"world-fixtures", "The test ran without a world snapshot; run it through the world helper so the effect has state to check."}},
	{reason: types.ReasonMissingEvidence, guideStep: guideStep{"trace-check", "The trace lacks the field this assertion reads; run `attest trace check` to see which spans lack it."}},
	{reason: types.ReasonSimulation, guideStep: guideStep{"simulation", "Simulation mode skips embedding and judge layers; run without ATTEST_SIMULATION to evaluate them."}},
	{reason: types.ReasonShortCircuited, guideStep: guideStep{"verdicts", "A deterministic assertion failed in the same batch; fix that failure first, this one was not run."}},
	{reason: types.ReasonProviderNotConfigured, guideStep: guideStep{"judges", "No judge or embedding provider is configured; set one up to evaluate this assertion, or keep the check deterministic."}},
	{reason: types.ReasonProviderUnavailable, guideStep: guideStep{"judges", "The provider could not be reached; retry, or check the provider configuration."}},
	{reason: types.ReasonBudgetExhausted, guideStep: guideStep{"judges", "The session budget ran out before this call; raise the budget or reduce judge assertions."}},
	{reason: types.ReasonJudgeAbstained, guideStep: guideStep{"judges", "The judge had too little evidence to score; add the missing evidence to the trace or rewrite the rubric."}},
	{typ: types.TypeEffect, status: types.StatusHardFail, guideStep: guideStep{"world-fixtures", "The agent's effect on the world broke the contract; fix the agent's code (for example send an idempotency key), then replay the fixture."}},
	{typ: types.TypeTrace, guideStep: guideStep{"behavior-contracts", "The agent's tool calls broke a behavior contract; fix the agent's code, or relax the contract if the behavior is intended."}},
	{typ: types.TypeSchema, guideStep: guideStep{"verdicts", "The output or a tool's arguments do not match the schema; fix the agent's output or update the schema."}},
	{typ: types.TypeConstraint, guideStep: guideStep{"verdicts", "A measured value crossed its limit; reduce the cost, latency or token use, or raise the limit on purpose."}},
	{typ: types.TypeContent, guideStep: guideStep{"verdicts", "The output text does not satisfy the content check; compare the output with the expected text."}},
	{typ: types.TypeLLMJudge, guideStep: guideStep{"judges", "The judge scored the output below the threshold; read the explanation and check the rubric."}},
	{typ: types.TypeEmbedding, guideStep: guideStep{"judges", "The output is not similar enough to the reference; compare them and check the threshold."}},
}

var fallbackStep = guideStep{"verdicts", "Read the explanation; `attest guide verdicts` describes how each status is decided."}

func stepFor(status, reason, typ string) guideStep {
	for _, r := range guideRules {
		if (r.reason == "" || r.reason == reason) && (r.typ == "" || r.typ == typ) && (r.status == "" || r.status == status) {
			return r.guideStep
		}
	}
	return fallbackStep
}

// guideTopics lists every topic the table can name, for the test that checks
// each one exists in the embedded guides.
func guideTopics() []string {
	seen := map[string]bool{fallbackStep.guide: true}
	out := []string{fallbackStep.guide}
	for _, r := range guideRules {
		if !seen[r.guide] {
			seen[r.guide] = true
			out = append(out, r.guide)
		}
	}
	return out
}
