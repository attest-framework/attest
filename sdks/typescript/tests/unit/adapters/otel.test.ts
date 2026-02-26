import { describe, it, expect } from "vitest";
import { OTelAdapter } from "../../../packages/core/src/adapters/otel.js";
import { STEP_LLM_CALL, STEP_TOOL_CALL } from "../../../packages/core/src/proto/constants.js";

function makeSpan(overrides: {
  name: string;
  startTime?: [number, number];
  endTime?: [number, number];
  attributes?: Record<string, unknown>;
  parentSpanId?: string;
  traceId?: string;
}) {
  return {
    name: overrides.name,
    startTime: overrides.startTime ?? [1000, 0],
    endTime: overrides.endTime ?? [1001, 0],
    attributes: overrides.attributes ?? {},
    parentSpanId: overrides.parentSpanId,
    spanContext: () => ({ traceId: overrides.traceId ?? "abcdef1234567890abcdef1234567890" }),
  };
}

describe("OTelAdapter.fromSpans()", () => {
  it("builds a trace from a single LLM span", () => {
    const span = makeSpan({
      name: "chat",
      attributes: {
        "gen_ai.operation.name": "chat",
        "gen_ai.completion": "Hello!",
        "gen_ai.request.model": "gpt-4.1",
        "gen_ai.usage.input_tokens": 10,
        "gen_ai.usage.output_tokens": 20,
      },
    });

    const trace = OTelAdapter.fromSpans([span], "otel-agent");
    expect(trace.agent_id).toBe("otel-agent");
    expect(trace.output.message).toBe("Hello!");
  });

  it("derives trace_id from root span", () => {
    const root = makeSpan({
      name: "root",
      traceId: "deadbeefdeadbeefdeadbeefdeadbeef",
      attributes: { "gen_ai.operation.name": "chat", "gen_ai.completion": "hi" },
    });

    const trace = OTelAdapter.fromSpans([root]);
    expect(trace.trace_id).toBe("otel_deadbeefdeadbeef");
  });

  it("identifies root span as the span without parentSpanId", () => {
    const root = makeSpan({
      name: "root",
      startTime: [1000, 0],
      endTime: [1002, 0],
      attributes: { "gen_ai.operation.name": "chat", "gen_ai.completion": "root response" },
    });
    const child = makeSpan({
      name: "child",
      startTime: [1000, 500_000_000],
      endTime: [1001, 0],
      parentSpanId: "span123",
      attributes: { "gen_ai.operation.name": "chat", "gen_ai.completion": "child" },
    });

    const trace = OTelAdapter.fromSpans([child, root]);
    expect(trace.output.message).toBe("child"); // last llm_call wins for output
  });

  it("adds llm_call step for chat operation", () => {
    const span = makeSpan({
      name: "my-chat",
      attributes: {
        "gen_ai.operation.name": "chat",
        "gen_ai.request.model": "gpt-4.1",
        "gen_ai.completion": "response text",
        "gen_ai.usage.input_tokens": 15,
        "gen_ai.usage.output_tokens": 25,
      },
    });

    const trace = OTelAdapter.fromSpans([span]);
    const llmStep = trace.steps.find((s) => s.type === STEP_LLM_CALL);
    expect(llmStep).toBeDefined();
    expect(llmStep?.name).toBe("my-chat");
    expect(llmStep?.args?.model).toBe("gpt-4.1");
    expect(llmStep?.result?.completion).toBe("response text");
    expect(llmStep?.result?.input_tokens).toBe(15);
    expect(llmStep?.result?.output_tokens).toBe(25);
  });

  it("adds tool_call step for tool operation", () => {
    const span = makeSpan({
      name: "tool-span",
      attributes: {
        "gen_ai.operation.name": "tool",
        "gen_ai.tool.name": "search",
        "gen_ai.tool.parameters": { q: "test" },
        "gen_ai.tool.output": "results",
      },
    });

    const trace = OTelAdapter.fromSpans([span]);
    const toolStep = trace.steps.find((s) => s.type === STEP_TOOL_CALL);
    expect(toolStep).toBeDefined();
    expect(toolStep?.name).toBe("search");
    expect(toolStep?.args?.parameters).toEqual({ q: "test" });
    expect(toolStep?.result?.output).toBe("results");
  });

  it("accumulates total_tokens across multiple LLM spans", () => {
    const span1 = makeSpan({
      name: "call-1",
      startTime: [1000, 0],
      endTime: [1001, 0],
      attributes: {
        "gen_ai.operation.name": "chat",
        "gen_ai.completion": "first",
        "gen_ai.usage.input_tokens": 10,
        "gen_ai.usage.output_tokens": 20,
      },
    });
    const span2 = makeSpan({
      name: "call-2",
      startTime: [1001, 0],
      endTime: [1002, 0],
      attributes: {
        "gen_ai.operation.name": "chat",
        "gen_ai.completion": "second",
        "gen_ai.usage.input_tokens": 5,
        "gen_ai.usage.output_tokens": 15,
      },
    });

    const trace = OTelAdapter.fromSpans([span1, span2]);
    expect(trace.metadata?.total_tokens).toBe(50); // 30 + 20
  });

  it("uses response model over request model in metadata", () => {
    const span = makeSpan({
      name: "completion",
      attributes: {
        "gen_ai.operation.name": "chat",
        "gen_ai.request.model": "gpt-4.1",
        "gen_ai.response.model": "gpt-4.1-2025-04-14",
        "gen_ai.completion": "hi",
      },
    });

    const trace = OTelAdapter.fromSpans([span]);
    expect(trace.metadata?.model).toBe("gpt-4.1-2025-04-14");
  });

  it("falls back to request model when response model absent", () => {
    const span = makeSpan({
      name: "completion",
      attributes: {
        "gen_ai.operation.name": "chat",
        "gen_ai.request.model": "gpt-4.1",
        "gen_ai.completion": "hi",
      },
    });

    const trace = OTelAdapter.fromSpans([span]);
    expect(trace.metadata?.model).toBe("gpt-4.1");
  });

  it("computes latency_ms from root span start/end", () => {
    const root = makeSpan({
      name: "root",
      startTime: [1000, 0],
      endTime: [1000, 500_000_000], // 500ms
      attributes: { "gen_ai.operation.name": "chat", "gen_ai.completion": "hi" },
    });

    const trace = OTelAdapter.fromSpans([root]);
    expect(trace.metadata?.latency_ms).toBe(500);
  });

  it("classifies spans by name when no gen_ai.operation.name attribute", () => {
    const span = makeSpan({
      name: "openai-completion",
      attributes: {
        "gen_ai.completion": "by name",
      },
    });

    const trace = OTelAdapter.fromSpans([span]);
    const llmStep = trace.steps.find((s) => s.type === STEP_LLM_CALL);
    expect(llmStep).toBeDefined();
  });

  it("ignores spans that cannot be classified", () => {
    const span = makeSpan({
      name: "http-request",
      attributes: { "http.status_code": 200 },
    });

    const trace = OTelAdapter.fromSpans([span]);
    expect(trace.steps).toHaveLength(0);
  });

  it("handles empty spans array", () => {
    const trace = OTelAdapter.fromSpans([]);
    expect(trace.output.message).toBe("");
    expect(trace.steps).toHaveLength(0);
    expect(trace.metadata?.total_tokens).toBeUndefined();
  });

  it("processes spans in chronological order", () => {
    const later = makeSpan({
      name: "second",
      startTime: [1001, 0],
      endTime: [1002, 0],
      attributes: { "gen_ai.operation.name": "chat", "gen_ai.completion": "second" },
    });
    const earlier = makeSpan({
      name: "first",
      startTime: [1000, 0],
      endTime: [1001, 0],
      attributes: { "gen_ai.operation.name": "chat", "gen_ai.completion": "first" },
    });

    const trace = OTelAdapter.fromSpans([later, earlier]);
    expect(trace.steps[0].name).toBe("first");
    expect(trace.steps[1].name).toBe("second");
  });

  it("step metadata includes duration_ms", () => {
    const span = makeSpan({
      name: "chat",
      startTime: [1000, 0],
      endTime: [1000, 200_000_000], // 200ms
      attributes: { "gen_ai.operation.name": "chat", "gen_ai.completion": "hi" },
    });

    const trace = OTelAdapter.fromSpans([span]);
    expect(trace.steps[0].metadata?.duration_ms).toBe(200);
  });
});
