import { describe, it, expect } from "vitest";
import { LangChainAdapter } from "../../../packages/core/src/adapters/langchain.js";

describe("LangChainAdapter", () => {
  it("builds a trace from accumulated LLM events", () => {
    const adapter = new LangChainAdapter("test-agent");

    adapter.handleChainStart();

    adapter.handleLLMEnd({
      generations: [[{ text: "Hello world" }]],
      llmOutput: {
        modelName: "gpt-4",
        tokenUsage: { totalTokens: 150 },
      },
    });

    adapter.handleChainEnd({ output: "Hello world" });

    const trace = adapter.buildTrace();

    expect(trace.agent_id).toBe("test-agent");
    expect(trace.output.message).toBe("Hello world");
    expect(trace.metadata?.model).toBe("gpt-4");
    expect(trace.metadata?.total_tokens).toBe(150);
    expect(trace.steps).toHaveLength(1);
    expect(trace.steps[0].type).toBe("llm_call");
  });

  it("captures tool calls", () => {
    const adapter = new LangChainAdapter();

    adapter.handleChainStart();
    adapter.handleLLMEnd({ generations: [[{ text: "searching..." }]] });
    adapter.handleToolEnd("42", { name: "calculator", output: "42" });
    adapter.handleChainEnd({ output: "The answer is 42" });

    const trace = adapter.buildTrace();

    expect(trace.steps).toHaveLength(2);
    expect(trace.steps[0].type).toBe("llm_call");
    expect(trace.steps[1].type).toBe("tool_call");
    expect(trace.steps[1].name).toBe("calculator");
    expect(trace.output.message).toBe("The answer is 42");
  });

  it("falls back to last LLM text when no chain output", () => {
    const adapter = new LangChainAdapter();

    adapter.handleLLMEnd({ generations: [[{ text: "fallback text" }]] });

    const trace = adapter.buildTrace();
    expect(trace.output.message).toBe("fallback text");
  });

  it("handles empty generations", () => {
    const adapter = new LangChainAdapter();

    adapter.handleLLMEnd({ generations: [[]] });

    const trace = adapter.buildTrace();
    expect(trace.output.message).toBe("");
  });

  it("accepts cost and model overrides", () => {
    const adapter = new LangChainAdapter();

    adapter.handleLLMEnd({ generations: [[{ text: "hi" }]] });

    const trace = adapter.buildTrace({ costUsd: 0.01, model: "gpt-4.1" });
    expect(trace.metadata?.cost_usd).toBe(0.01);
    expect(trace.metadata?.model).toBe("gpt-4.1");
  });

  it("reset clears accumulated state", () => {
    const adapter = new LangChainAdapter();

    adapter.handleChainStart();
    adapter.handleLLMEnd({ generations: [[{ text: "first" }]] });

    adapter.reset();

    adapter.handleLLMEnd({ generations: [[{ text: "second" }]] });
    const trace = adapter.buildTrace();

    expect(trace.steps).toHaveLength(1);
    expect(trace.output.message).toBe("second");
  });

  it("records latency from chain start to build", () => {
    const adapter = new LangChainAdapter();

    adapter.handleChainStart();
    adapter.handleLLMEnd({ generations: [[{ text: "hi" }]] });

    const trace = adapter.buildTrace();
    expect(trace.metadata?.latency_ms).toBeGreaterThanOrEqual(0);
  });

  it("accumulates tokens across multiple LLM calls", () => {
    const adapter = new LangChainAdapter();

    adapter.handleLLMEnd({
      generations: [[{ text: "a" }]],
      llmOutput: { tokenUsage: { totalTokens: 100 } },
    });
    adapter.handleLLMEnd({
      generations: [[{ text: "b" }]],
      llmOutput: { tokenUsage: { totalTokens: 200 } },
    });

    const trace = adapter.buildTrace();
    expect(trace.metadata?.total_tokens).toBe(300);
    expect(trace.steps).toHaveLength(2);
  });
});
