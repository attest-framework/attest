import { describe, it, expect } from "vitest";
import { OllamaAdapter } from "../../../packages/core/src/adapters/ollama.js";
import { STEP_LLM_CALL } from "../../../packages/core/src/proto/constants.js";

describe("OllamaAdapter", () => {
  const adapter = new OllamaAdapter("ollama-agent");

  const baseResponse = {
    model: "llama3.2",
    message: { content: "Hello from Ollama!" },
    eval_count: 80,
    prompt_eval_count: 20,
  };

  it("builds a trace with correct trace_id format", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.trace_id).toMatch(/^trc_[a-f0-9]{12}$/);
  });

  it("sets agent_id from constructor", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.agent_id).toBe("ollama-agent");
  });

  it("extracts completion from message content", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.output.message).toBe("Hello from Ollama!");
  });

  it("adds llm_call step with model and completion", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    const llmStep = trace.steps.find((s) => s.type === STEP_LLM_CALL);
    expect(llmStep).toBeDefined();
    expect(llmStep?.name).toBe("completion");
    expect(llmStep?.args?.model).toBe("llama3.2");
    expect(llmStep?.result?.completion).toBe("Hello from Ollama!");
  });

  it("sums eval_count and prompt_eval_count for total_tokens", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.metadata?.total_tokens).toBe(100); // 80 + 20
  });

  it("sets model in metadata", () => {
    const trace = adapter.traceFromResponse(baseResponse);
    expect(trace.metadata?.model).toBe("llama3.2");
  });

  it("sets latency from options", () => {
    const trace = adapter.traceFromResponse(baseResponse, { latencyMs: 350 });
    expect(trace.metadata?.latency_ms).toBe(350);
  });

  it("sets input messages when provided", () => {
    const trace = adapter.traceFromResponse(baseResponse, {
      inputMessages: [{ role: "user", content: "hello" }],
    });
    expect(trace.input).toEqual({ messages: [{ role: "user", content: "hello" }] });
  });

  it("handles missing message content with empty string", () => {
    const response = { model: "llama3.2", eval_count: 10, prompt_eval_count: 5 };
    const trace = adapter.traceFromResponse(response);
    expect(trace.output.message).toBe("");
  });

  it("omits total_tokens when only one count is present", () => {
    const response = { model: "llama3.2", message: { content: "hi" }, eval_count: 50 };
    const trace = adapter.traceFromResponse(response);
    expect(trace.metadata?.total_tokens).toBeUndefined();
  });

  it("omits total_tokens when neither count is present", () => {
    const response = { model: "llama3.2", message: { content: "hi" } };
    const trace = adapter.traceFromResponse(response);
    expect(trace.metadata?.total_tokens).toBeUndefined();
  });

  it("uses empty string for model when missing from response", () => {
    const response = { message: { content: "hi" }, eval_count: 10, prompt_eval_count: 5 };
    const trace = adapter.traceFromResponse(response);
    const llmStep = trace.steps.find((s) => s.type === STEP_LLM_CALL);
    expect(llmStep?.args?.model).toBe("");
  });

  it("works without agentId", () => {
    const noIdAdapter = new OllamaAdapter();
    const trace = noIdAdapter.traceFromResponse(baseResponse);
    expect(trace.agent_id).toBeUndefined();
  });
});
