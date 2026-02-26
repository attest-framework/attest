import { describe, it, expect } from "vitest";
import { GeminiAdapter } from "../../../packages/core/src/adapters/gemini.js";
import { STEP_LLM_CALL, STEP_TOOL_CALL } from "../../../packages/core/src/proto/constants.js";

describe("GeminiAdapter", () => {
  const adapter = new GeminiAdapter("gemini-agent");

  it("builds a trace with correct trace_id format", () => {
    const trace = adapter.traceFromResponse({ text: "Hello!" });
    expect(trace.trace_id).toMatch(/^trc_[a-f0-9]{12}$/);
  });

  it("sets agent_id from constructor", () => {
    const trace = adapter.traceFromResponse({ text: "Hello!" });
    expect(trace.agent_id).toBe("gemini-agent");
  });

  it("extracts completion from top-level text field", () => {
    const trace = adapter.traceFromResponse({ text: "Direct text response" });
    expect(trace.output.message).toBe("Direct text response");
  });

  it("extracts completion from candidates when text not present", () => {
    const response = {
      candidates: [
        {
          content: {
            parts: [{ text: "Part A" }, { text: "Part B" }],
          },
        },
      ],
    };
    const trace = adapter.traceFromResponse(response);
    expect(trace.output.message).toBe("Part APart B");
  });

  it("prefers top-level text over candidates", () => {
    const response = {
      text: "Direct",
      candidates: [
        {
          content: {
            parts: [{ text: "From candidates" }],
          },
        },
      ],
    };
    const trace = adapter.traceFromResponse(response);
    expect(trace.output.message).toBe("Direct");
  });

  it("adds llm_call step", () => {
    const trace = adapter.traceFromResponse({ text: "hi" });
    const llmStep = trace.steps.find((s) => s.type === STEP_LLM_CALL);
    expect(llmStep).toBeDefined();
    expect(llmStep?.name).toBe("completion");
    expect(llmStep?.result?.completion).toBe("hi");
  });

  it("adds tool_call steps for function calls in candidates", () => {
    const response = {
      candidates: [
        {
          content: {
            parts: [
              {
                functionCall: {
                  name: "get_weather",
                  args: { location: "Tokyo" },
                },
              },
              { text: "Checking weather..." },
            ],
          },
        },
      ],
    };
    const trace = adapter.traceFromResponse(response);
    const toolSteps = trace.steps.filter((s) => s.type === STEP_TOOL_CALL);
    expect(toolSteps).toHaveLength(1);
    expect(toolSteps[0].name).toBe("get_weather");
    expect(toolSteps[0].args).toEqual({ location: "Tokyo" });
  });

  it("handles function calls with no args", () => {
    const response = {
      candidates: [
        {
          content: {
            parts: [
              {
                functionCall: {
                  name: "ping",
                },
              },
            ],
          },
        },
      ],
    };
    const trace = adapter.traceFromResponse(response);
    const toolSteps = trace.steps.filter((s) => s.type === STEP_TOOL_CALL);
    expect(toolSteps).toHaveLength(1);
    expect(toolSteps[0].args).toEqual({});
  });

  it("sets model from options", () => {
    const trace = adapter.traceFromResponse({ text: "hi" }, { model: "gemini-2.0-flash" });
    expect(trace.metadata?.model).toBe("gemini-2.0-flash");
  });

  it("sets input text when provided", () => {
    const trace = adapter.traceFromResponse({ text: "response" }, { inputText: "what is 2+2?" });
    expect(trace.input).toEqual({ text: "what is 2+2?" });
  });

  it("includes cost and latency from options", () => {
    const trace = adapter.traceFromResponse({ text: "ok" }, { costUsd: 0.001, latencyMs: 500 });
    expect(trace.metadata?.cost_usd).toBe(0.001);
    expect(trace.metadata?.latency_ms).toBe(500);
  });

  it("handles empty response", () => {
    const trace = adapter.traceFromResponse({});
    expect(trace.output.message).toBe("");
    expect(trace.steps.filter((s) => s.type === STEP_TOOL_CALL)).toHaveLength(0);
  });

  it("works without agentId", () => {
    const noIdAdapter = new GeminiAdapter();
    const trace = noIdAdapter.traceFromResponse({ text: "hi" });
    expect(trace.agent_id).toBeUndefined();
  });
});
