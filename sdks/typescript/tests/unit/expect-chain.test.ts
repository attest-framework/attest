import { describe, it, expect } from "vitest";
import { TraceBuilder } from "../../packages/core/src/trace.js";
import { AgentResult } from "../../packages/core/src/result.js";
import { ExpectChain, attestExpect } from "../../packages/core/src/expect.js";
import {
  TYPE_SCHEMA,
  TYPE_CONSTRAINT,
  TYPE_TRACE,
  TYPE_CONTENT,
  TYPE_EMBEDDING,
  TYPE_LLM_JUDGE,
  TYPE_TRACE_TREE,
  TYPE_PLUGIN,
} from "../../packages/core/src/proto/constants.js";

function makeResult(): AgentResult {
  const trace = new TraceBuilder("test-agent")
    .setInput({ query: "hello" })
    .addLlmCall("gpt-4", { result: { text: "hi" } })
    .setOutput({ message: "hi there" })
    .build();
  return new AgentResult(trace);
}

describe("ExpectChain", () => {
  it("is created via attestExpect()", () => {
    const chain = attestExpect(makeResult());
    expect(chain).toBeInstanceOf(ExpectChain);
    expect(chain.assertions).toHaveLength(0);
  });

  it("exposes the trace from the result", () => {
    const result = makeResult();
    const chain = attestExpect(result);
    expect(chain.trace).toBe(result.trace);
  });

  it("accepts a Trace directly and wraps it in AgentResult", () => {
    const trace = new TraceBuilder("test-agent")
      .setInput({ query: "hello" })
      .setOutput({ message: "hi" })
      .build();
    const chain = attestExpect(trace);
    expect(chain).toBeInstanceOf(ExpectChain);
    expect(chain.trace).toBe(trace);
    expect(chain.assertions).toHaveLength(0);
  });

  // Layer 1: Schema
  it("outputMatchesSchema adds schema assertion", () => {
    const chain = attestExpect(makeResult()).outputMatchesSchema({ type: "object" });
    expect(chain.assertions).toHaveLength(1);
    expect(chain.assertions[0].type).toBe(TYPE_SCHEMA);
    expect(chain.assertions[0].spec.target).toBe("output.structured");
  });

  it("toolArgsMatchSchema targets correct step", () => {
    const chain = attestExpect(makeResult()).toolArgsMatchSchema("search", { type: "object" });
    expect(chain.assertions[0].spec.target).toBe("steps[?name=='search'].args");
  });

  // Layer 2: Constraints
  it("costUnder adds constraint assertion", () => {
    const chain = attestExpect(makeResult()).costUnder(0.01);
    expect(chain.assertions[0].type).toBe(TYPE_CONSTRAINT);
    expect(chain.assertions[0].spec.field).toBe("metadata.cost_usd");
    expect(chain.assertions[0].spec.operator).toBe("lte");
    expect(chain.assertions[0].spec.value).toBe(0.01);
    expect(chain.assertions[0].spec.soft).toBe(false);
  });

  it("costUnder supports soft option", () => {
    const chain = attestExpect(makeResult()).costUnder(0.01, { soft: true });
    expect(chain.assertions[0].spec.soft).toBe(true);
  });

  it("tokensBetween sets min and max", () => {
    const chain = attestExpect(makeResult()).tokensBetween(100, 500);
    expect(chain.assertions[0].spec.operator).toBe("between");
    expect(chain.assertions[0].spec.min).toBe(100);
    expect(chain.assertions[0].spec.max).toBe(500);
  });

  // Layer 3: Trace
  it("toolsCalledInOrder adds trace assertion", () => {
    const chain = attestExpect(makeResult()).toolsCalledInOrder(["search", "write"]);
    expect(chain.assertions[0].type).toBe(TYPE_TRACE);
    expect(chain.assertions[0].spec.check).toBe("contains_in_order");
    expect(chain.assertions[0].spec.tools).toEqual(["search", "write"]);
  });

  it("forbiddenTools adds trace assertion", () => {
    const chain = attestExpect(makeResult()).forbiddenTools(["delete"]);
    expect(chain.assertions[0].spec.check).toBe("forbidden_tools");
  });

  // Layer 4: Content
  it("outputContains adds content assertion", () => {
    const chain = attestExpect(makeResult()).outputContains("hello");
    expect(chain.assertions[0].type).toBe(TYPE_CONTENT);
    expect(chain.assertions[0].spec.check).toBe("contains");
    expect(chain.assertions[0].spec.value).toBe("hello");
  });

  it("outputForbids adds forbidden content assertion", () => {
    const chain = attestExpect(makeResult()).outputForbids(["password", "secret"]);
    expect(chain.assertions[0].spec.check).toBe("forbidden");
    expect(chain.assertions[0].spec.values).toEqual(["password", "secret"]);
  });

  // Layer 5: Embedding
  it("outputSimilarTo adds embedding assertion", () => {
    const chain = attestExpect(makeResult()).outputSimilarTo("greeting");
    expect(chain.assertions[0].type).toBe(TYPE_EMBEDDING);
    expect(chain.assertions[0].spec.threshold).toBe(0.8);
  });

  // Layer 6: LLM Judge
  it("passesJudge adds llm_judge assertion", () => {
    const chain = attestExpect(makeResult()).passesJudge("is polite");
    expect(chain.assertions[0].type).toBe(TYPE_LLM_JUDGE);
    expect(chain.assertions[0].spec.criteria).toBe("is polite");
    expect(chain.assertions[0].spec.rubric).toBe("default");
  });

  // Layer 7: Trace Tree
  it("agentCalled adds trace_tree assertion", () => {
    const chain = attestExpect(makeResult()).agentCalled("sub-agent");
    expect(chain.assertions[0].type).toBe(TYPE_TRACE_TREE);
    expect(chain.assertions[0].spec.check).toBe("agent_called");
  });

  it("followsTransitions adds trace_tree assertion", () => {
    const chain = attestExpect(makeResult()).followsTransitions([
      ["root", "child-a"],
      ["child-a", "child-b"],
    ]);
    expect(chain.assertions[0].spec.check).toBe("follows_transitions");
    expect(chain.assertions[0].spec.transitions).toEqual([
      ["root", "child-a"],
      ["child-a", "child-b"],
    ]);
  });

  // Chaining
  it("supports fluent chaining across layers", () => {
    const chain = attestExpect(makeResult())
      .outputContains("hi")
      .costUnder(0.05)
      .toolsCalledInOrder(["gpt-4"])
      .agentCalled("sub")
      .passesJudge("is helpful");

    expect(chain.assertions).toHaveLength(5);
    expect(chain.assertions[0].type).toBe(TYPE_CONTENT);
    expect(chain.assertions[1].type).toBe(TYPE_CONSTRAINT);
    expect(chain.assertions[2].type).toBe(TYPE_TRACE);
    expect(chain.assertions[3].type).toBe(TYPE_TRACE_TREE);
    expect(chain.assertions[4].type).toBe(TYPE_LLM_JUDGE);
  });

  it("generates unique assertion IDs", () => {
    const chain = attestExpect(makeResult())
      .outputContains("a")
      .outputContains("b");

    const ids = chain.assertions.map((a) => a.assertion_id);
    expect(new Set(ids).size).toBe(2);
    expect(ids[0]).toMatch(/^assert_[a-f0-9]{8}$/);
  });

  // Layer 7: Temporal DSL
  it("agentOrderedBefore adds trace_tree assertion", () => {
    const chain = attestExpect(makeResult()).agentOrderedBefore("planner", "executor");
    expect(chain.assertions).toHaveLength(1);
    expect(chain.assertions[0].type).toBe(TYPE_TRACE_TREE);
    expect(chain.assertions[0].spec.check).toBe("agent_ordered_before");
    expect(chain.assertions[0].spec.agent_a).toBe("planner");
    expect(chain.assertions[0].spec.agent_b).toBe("executor");
    expect(chain.assertions[0].spec.soft).toBe(false);
  });

  it("agentOrderedBefore supports soft option", () => {
    const chain = attestExpect(makeResult()).agentOrderedBefore("a", "b", { soft: true });
    expect(chain.assertions[0].spec.soft).toBe(true);
  });

  it("agentsOverlap adds trace_tree assertion", () => {
    const chain = attestExpect(makeResult()).agentsOverlap("worker-1", "worker-2");
    expect(chain.assertions).toHaveLength(1);
    expect(chain.assertions[0].type).toBe(TYPE_TRACE_TREE);
    expect(chain.assertions[0].spec.check).toBe("agents_overlap");
    expect(chain.assertions[0].spec.agent_a).toBe("worker-1");
    expect(chain.assertions[0].spec.agent_b).toBe("worker-2");
    expect(chain.assertions[0].spec.soft).toBe(false);
  });

  it("agentsOverlap supports soft option", () => {
    const chain = attestExpect(makeResult()).agentsOverlap("a", "b", { soft: true });
    expect(chain.assertions[0].spec.soft).toBe(true);
  });

  it("agentWallTimeUnder adds trace_tree assertion", () => {
    const chain = attestExpect(makeResult()).agentWallTimeUnder("summarizer", 3000);
    expect(chain.assertions).toHaveLength(1);
    expect(chain.assertions[0].type).toBe(TYPE_TRACE_TREE);
    expect(chain.assertions[0].spec.check).toBe("agent_wall_time_under");
    expect(chain.assertions[0].spec.agent_id).toBe("summarizer");
    expect(chain.assertions[0].spec.max_ms).toBe(3000);
    expect(chain.assertions[0].spec.soft).toBe(false);
  });

  it("agentWallTimeUnder supports soft option", () => {
    const chain = attestExpect(makeResult()).agentWallTimeUnder("summarizer", 3000, { soft: true });
    expect(chain.assertions[0].spec.soft).toBe(true);
  });

  it("orderedAgents adds trace_tree assertion", () => {
    const groups = [["planner"], ["worker-1", "worker-2"], ["aggregator"]];
    const chain = attestExpect(makeResult()).orderedAgents(groups);
    expect(chain.assertions).toHaveLength(1);
    expect(chain.assertions[0].type).toBe(TYPE_TRACE_TREE);
    expect(chain.assertions[0].spec.check).toBe("ordered_agents");
    expect(chain.assertions[0].spec.groups).toEqual(groups);
    expect(chain.assertions[0].spec.soft).toBe(false);
  });

  it("orderedAgents supports soft option", () => {
    const chain = attestExpect(makeResult()).orderedAgents([["a"], ["b"]], { soft: true });
    expect(chain.assertions[0].spec.soft).toBe(true);
  });

  it("temporal methods support fluent chaining", () => {
    const chain = attestExpect(makeResult())
      .agentOrderedBefore("planner", "executor")
      .agentsOverlap("worker-1", "worker-2")
      .agentWallTimeUnder("summarizer", 5000)
      .orderedAgents([["a"], ["b", "c"]]);

    expect(chain.assertions).toHaveLength(4);
    const checks = chain.assertions.map((a) => a.spec.check);
    expect(checks).toEqual([
      "agent_ordered_before",
      "agents_overlap",
      "agent_wall_time_under",
      "ordered_agents",
    ]);
  });

  // Layer 8: Plugin
  it("plugin adds plugin assertion", () => {
    const chain = attestExpect(makeResult()).plugin("safety-check");
    expect(chain.assertions).toHaveLength(1);
    expect(chain.assertions[0].type).toBe(TYPE_PLUGIN);
    expect(chain.assertions[0].spec.plugin_id).toBe("safety-check");
    expect(chain.assertions[0].spec.soft).toBe(false);
  });

  it("plugin accepts config and soft option", () => {
    const chain = attestExpect(makeResult()).plugin(
      "custom-check",
      { threshold: 0.95 },
      { soft: true },
    );
    expect(chain.assertions[0].spec.config).toEqual({ threshold: 0.95 });
    expect(chain.assertions[0].spec.soft).toBe(true);
  });

  // TraceTree analytics DSL
  it("aggregateLatencyUnder adds trace_tree assertion", () => {
    const chain = attestExpect(makeResult()).aggregateLatencyUnder(5000);
    expect(chain.assertions).toHaveLength(1);
    expect(chain.assertions[0].type).toBe(TYPE_TRACE_TREE);
    expect(chain.assertions[0].spec.check).toBe("aggregate_latency");
    expect(chain.assertions[0].spec.value).toBe(5000);
  });

  it("allToolsCalled adds trace_tree assertion", () => {
    const chain = attestExpect(makeResult()).allToolsCalled(["search", "summarize"]);
    expect(chain.assertions).toHaveLength(1);
    expect(chain.assertions[0].type).toBe(TYPE_TRACE_TREE);
    expect(chain.assertions[0].spec.check).toBe("all_tools_called");
    expect(chain.assertions[0].spec.tools).toEqual(["search", "summarize"]);
  });
});
