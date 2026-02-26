import { describe, it, expect } from "vitest";
import { AnthropicAdapter } from "../../../packages/core/src/adapters/anthropic.js";
import { STEP_LLM_CALL, STEP_TOOL_CALL } from "../../../packages/core/src/proto/constants.js";

describe("AnthropicAdapter", () => {
  const adapter = new AnthropicAdapter("claude-agent");

  const baseResponse = {
    model: "claude-opus-4-6",
    usage: { input_tokens: 50, output_tokens: 100 },
    content: [{ type: "text" as const, text: "Hello from Claude!" }],
  };

  it("builds a trace with correct trace_id format", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.trace_id).toMatch(/^trc_[a-f0-9]{12}$/);
  });

  it("sets agent_id from constructor", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.agent_id).toBe("claude-agent");
  });

  it("concatenates text blocks into completion", () => {
    const response = {
      model: "claude-opus-4-6",
      usage: { input_tokens: 10, output_tokens: 20 },
      content: [
        { type: "text" as const, text: "Part one. " },
        { type: "text" as const, text: "Part two." },
      ],
    };
    const trace = adapter.traceFromResponse(response);
    expect(trace.output.message).toBe("Part one. \nPart two.");
  });

  it("adds llm_call step after processing content", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    const llmStep = trace.steps.find((s) => s.type === STEP_LLM_CALL);
    expect(llmStep).toBeDefined();
    expect(llmStep?.name).toBe("completion");
    expect(llmStep?.args?.model).toBe("claude-opus-4-6");
    expect(llmStep?.result?.completion).toBe("Hello from Claude!");
  });

  it("sums input and output tokens for total_tokens", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.metadata?.total_tokens).toBe(150); // 50 + 100
  });

  it("adds tool_call steps for tool_use blocks", () => {
    const response = {
      model: "claude-opus-4-6",
      usage: { input_tokens: 20, output_tokens: 30 },
      content: [
        {
          type: "tool_use" as const,
          name: "get_weather",
          input: { location: "San Francisco" },
        },
        { type: "text" as const, text: "Let me check the weather." },
      ],
    };
    const trace = adapter.traceFromResponse(response);
    const toolSteps = trace.steps.filter((s) => s.type === STEP_TOOL_CALL);
    expect(toolSteps).toHaveLength(1);
    expect(toolSteps[0].name).toBe("get_weather");
    expect(toolSteps[0].args).toEqual({ location: "San Francisco" });
  });

  it("handles multiple tool_use blocks", () => {
    const response = {
      model: "claude-opus-4-6",
      usage: { input_tokens: 10, output_tokens: 10 },
      content: [
        { type: "tool_use" as const, name: "search", input: { q: "test" } },
        { type: "tool_use" as const, name: "fetch", input: { url: "http://example.com" } },
      ],
    };
    const trace = adapter.traceFromResponse(response);
    const toolSteps = trace.steps.filter((s) => s.type === STEP_TOOL_CALL);
    expect(toolSteps).toHaveLength(2);
    expect(toolSteps[0].name).toBe("search");
    expect(toolSteps[1].name).toBe("fetch");
  });

  it("sets input messages when provided", () => {
    const trace = adapter.traceFromResponse(baseResponse, {
      inputMessages: [{ role: "user", content: "hello" }],
    });
    expect(trace.input).toEqual({ messages: [{ role: "user", content: "hello" }] });
  });

  it("includes cost and latency from options", () => {
    const trace = adapter.traceFromResponse(baseResponse, {
      costUsd: 0.005,
      latencyMs: 1200,
    });
    expect(trace.metadata?.cost_usd).toBe(0.005);
    expect(trace.metadata?.latency_ms).toBe(1200);
  });

  it("handles missing usage gracefully", () => {
    const response = {
      model: "claude-opus-4-6",
      content: [{ type: "text" as const, text: "hi" }],
    };
    const trace = adapter.traceFromResponse(response);
    expect(trace.metadata?.total_tokens).toBeUndefined();
  });

  it("handles empty content array", () => {
    const response = {
      model: "claude-opus-4-6",
      usage: { input_tokens: 5, output_tokens: 0 },
      content: [] as never[],
    };
    const trace = adapter.traceFromResponse(response);
    expect(trace.output.message).toBe("");
  });

  it("works without agentId", () => {
    const noIdAdapter = new AnthropicAdapter();
    const trace = noIdAdapter.traceFromResponse(baseResponse);
    expect(trace.agent_id).toBeUndefined();
  });
});
