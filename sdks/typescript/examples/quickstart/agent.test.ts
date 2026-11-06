/**
 * Attest TypeScript SDK Quickstart
 *
 * Demonstrates how to test an AI agent using Attest assertions.
 * Run: pnpm test
 */
import { describe, it, expect } from "vitest";
import {
  Agent,
  TraceBuilder,
  AgentResult,
  attestExpect,
  TraceTree,
} from "@attest-ai/core";

// -- Define a simple agent --

const qaAgent = new Agent("qa-agent", (builder: TraceBuilder, args: Record<string, unknown>) => {
  const query = String(args.query ?? "");

  // Simulate an LLM call
  builder.addLlmCall("gpt-4", {
    args: { model: "gpt-4", prompt: query },
    result: { completion: `The answer to "${query}" is 42.` },
  });

  // Simulate a tool call
  builder.addToolCall("fact-check", {
    args: { claim: "The answer is 42" },
    result: { verified: true },
  });

  builder.setMetadata({ total_tokens: 150, cost_usd: 0.002, latency_ms: 350, model: "gpt-4" });

  return { message: `The answer to "${query}" is 42.` };
});

// -- Tests --

describe("QA Agent", () => {
  it("answers a question and stays within cost budget", () => {
    const result = qaAgent.run({ query: "What is the meaning of life?" });

    expect(result.passed).toBe(true);
    expect(result.trace.steps).toHaveLength(2);

    const chain = attestExpect(result)
      .outputContains("42")
      .costUnder(0.01)
      .tokensUnder(500)
      .toolsCalledInOrder(["gpt-4", "fact-check"])
      .requiredTools(["fact-check"]);

    // Assertions collected — in a full integration test,
    // chain.assertions would be sent to the engine for evaluation.
    expect(chain.assertions).toHaveLength(5);
  });

  it("forbids sensitive content in output", () => {
    const result = qaAgent.run({ query: "Tell me a secret" });

    const chain = attestExpect(result)
      .outputForbids(["password", "secret_key", "credit_card"])
      .outputNotContains("confidential");

    expect(chain.assertions).toHaveLength(2);
  });

  it("can be tested with soft failure thresholds", () => {
    const result = qaAgent.run({ query: "Quick question" });

    const chain = attestExpect(result)
      .latencyUnder(100, { soft: true })  // soft: warn but don't fail
      .costUnder(0.001, { soft: true });

    expect(chain.assertions[0].spec.soft).toBe(true);
    expect(chain.assertions[1].spec.soft).toBe(true);
  });
});

// -- Multi-agent example --

describe("Multi-Agent Trace Tree", () => {
  it("analyzes delegation chains", () => {
    // Build a multi-agent trace manually
    const trace = new TraceBuilder("orchestrator")
      .setInput({ task: "research and summarize" })
      .addStep({
        type: "agent_call",
        name: "researcher",
        result: { findings: "found relevant data" },
        sub_trace: new TraceBuilder("researcher")
          .addToolCall("web-search", {
            args: { q: "AI testing frameworks" },
            result: { results: ["attest", "promptfoo"] },
          })
          .setOutput({ findings: "found relevant data" })
          .setMetadata({ total_tokens: 200, cost_usd: 0.005 })
          .build(),
      })
      .setOutput({ summary: "AI testing frameworks include Attest and Promptfoo." })
      .setMetadata({ total_tokens: 100, cost_usd: 0.003 })
      .build();

    const tree = new TraceTree(trace);

    // Verify tree structure
    expect(tree.agents).toEqual(["orchestrator", "researcher"]);
    expect(tree.depth).toBe(1);
    expect(tree.delegations).toEqual([["orchestrator", "researcher"]]);
    expect(tree.allToolCalls()).toHaveLength(1);
    expect(tree.aggregateTokens).toBe(300);
    expect(tree.aggregateCost).toBeCloseTo(0.008);

    // Assertion chain for multi-agent
    const result = new AgentResult(trace);
    const chain = attestExpect(result)
      .agentCalled("researcher")
      .delegationDepth(2)
      .aggregateCostUnder(0.05)
      .followsTransitions([["orchestrator", "researcher"]]);

    expect(chain.assertions).toHaveLength(4);
  });
});
