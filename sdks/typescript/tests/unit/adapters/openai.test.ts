import { describe, it, expect } from "vitest";
import { OpenAIAdapter } from "../../../packages/core/src/adapters/openai.js";
import { STEP_LLM_CALL, STEP_TOOL_CALL } from "../../../packages/core/src/proto/constants.js";

describe("OpenAIAdapter", () => {
  const adapter = new OpenAIAdapter("test-agent");

  const baseResponse = {
    model: "gpt-4.1",
    usage: { total_tokens: 150 },
    choices: [
      {
        message: {
          content: "Hello, world!",
          tool_calls: undefined,
        },
      },
    ],
  };

  it("builds a trace with correct trace_id format", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.trace_id).toMatch(/^trc_[a-f0-9]{12}$/);
  });

  it("sets agent_id from constructor", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.agent_id).toBe("test-agent");
  });

  it("adds llm_call step with model and token info", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    const llmStep = trace.steps.find((s) => s.type === STEP_LLM_CALL);
    expect(llmStep).toBeDefined();
    expect(llmStep?.name).toBe("completion");
    expect(llmStep?.args?.model).toBe("gpt-4.1");
    expect(llmStep?.result?.completion).toBe("Hello, world!");
    expect(llmStep?.result?.tokens).toBe(150);
  });

  it("sets output message from completion text", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.output.message).toBe("Hello, world!");
  });

  it("sets metadata with tokens and model", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.metadata?.total_tokens).toBe(150);
    expect(trace.metadata?.model).toBe("gpt-4.1");
  });

  it("includes cost and latency from options", () => {
    const trace = adapter.traceFromResponse(baseResponse, {
      costUsd: 0.002,
      latencyMs: 800,
    });
    expect(trace.metadata?.cost_usd).toBe(0.002);
    expect(trace.metadata?.latency_ms).toBe(800);
  });

  it("adds tool_call steps for each tool call", () => {
    const response = {
      ...baseResponse,
      choices: [
        {
          message: {
            content: null,
            tool_calls: [
              { function: { name: "search", arguments: '{"q":"test"}' } },
              { function: { name: "fetch", arguments: '{"url":"http://example.com"}' } },
            ],
          },
        },
      ],
    };

    const trace = adapter.traceFromResponse(response);
    const toolSteps = trace.steps.filter((s) => s.type === STEP_TOOL_CALL);
    expect(toolSteps).toHaveLength(2);
    expect(toolSteps[0].name).toBe("search");
    expect(toolSteps[0].args?.arguments).toBe('{"q":"test"}');
    expect(toolSteps[1].name).toBe("fetch");
  });

  it("sets input messages when provided", () => {
    const trace = adapter.traceFromResponse(baseResponse, {
      inputMessages: [{ role: "user", content: "hello" }],
    });
    expect(trace.input).toEqual({ messages: [{ role: "user", content: "hello" }] });
  });

  it("includes structured output in output field", () => {
    const trace = adapter.traceFromResponse(baseResponse, {
      structuredOutput: { intent: "greeting" },
    });
    expect(trace.output.structured).toEqual({ intent: "greeting" });
  });

  it("handles missing usage gracefully", () => {
    const response = {
      model: "gpt-4.1",
      choices: [{ message: { content: "hi" } }],
    };
    const trace = adapter.traceFromResponse(response);
    expect(trace.metadata?.total_tokens).toBeUndefined();
  });

  it("handles null message content", () => {
    const response = {
      model: "gpt-4.1",
      choices: [{ message: { content: null } }],
    };
    const trace = adapter.traceFromResponse(response);
    expect(trace.output.message).toBe("");
  });

  it("works without agentId", () => {
    const noIdAdapter = new OpenAIAdapter();
    const trace = noIdAdapter.traceFromResponse(baseResponse);
    expect(trace.agent_id).toBeUndefined();
  });
});
