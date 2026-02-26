import { describe, it, expect } from "vitest";
import { ManualAdapter } from "../../../packages/core/src/adapters/manual.js";
import { TraceBuilder } from "../../../packages/core/src/trace.js";
import { STEP_LLM_CALL, STEP_TOOL_CALL } from "../../../packages/core/src/proto/constants.js";

describe("ManualAdapter", () => {
  const adapter = new ManualAdapter("manual-agent");

  describe("capture()", () => {
    it("builds trace using the provided builder function", () => {
      const trace = adapter.capture((builder) => {
        builder.addLlmCall("gpt-4.1", { result: { text: "hello" } });
        builder.setOutput({ message: "hello" });
      });

      expect(trace.trace_id).toMatch(/^trc_[a-f0-9]{12}$/);
      expect(trace.agent_id).toBe("manual-agent");
      expect(trace.output).toEqual({ message: "hello" });
    });

    it("captures llm_call steps", () => {
      const trace = adapter.capture((builder) => {
        builder.addLlmCall("gpt-4.1", {
          args: { prompt: "Say hello" },
          result: { completion: "Hello!" },
        });
        builder.setOutput({ message: "Hello!" });
      });

      expect(trace.steps).toHaveLength(1);
      expect(trace.steps[0].type).toBe(STEP_LLM_CALL);
      expect(trace.steps[0].name).toBe("gpt-4.1");
    });

    it("captures tool_call steps", () => {
      const trace = adapter.capture((builder) => {
        builder.addToolCall("search", { args: { query: "test" }, result: { hits: 5 } });
        builder.setOutput({ done: true });
      });

      expect(trace.steps).toHaveLength(1);
      expect(trace.steps[0].type).toBe(STEP_TOOL_CALL);
      expect(trace.steps[0].name).toBe("search");
      expect(trace.steps[0].args).toEqual({ query: "test" });
      expect(trace.steps[0].result).toEqual({ hits: 5 });
    });

    it("captures multiple steps in order", () => {
      const trace = adapter.capture((builder) => {
        builder.addLlmCall("gpt-4.1");
        builder.addToolCall("fetch");
        builder.addLlmCall("gpt-4.1");
        builder.setOutput({ done: true });
      });

      expect(trace.steps).toHaveLength(3);
      expect(trace.steps[0].type).toBe(STEP_LLM_CALL);
      expect(trace.steps[1].type).toBe(STEP_TOOL_CALL);
      expect(trace.steps[2].type).toBe(STEP_LLM_CALL);
    });

    it("captures metadata set within builder function", () => {
      const trace = adapter.capture((builder) => {
        builder.setMetadata({ total_tokens: 200, model: "gpt-4.1", latency_ms: 600 });
        builder.setOutput({ done: true });
      });

      expect(trace.metadata?.total_tokens).toBe(200);
      expect(trace.metadata?.model).toBe("gpt-4.1");
      expect(trace.metadata?.latency_ms).toBe(600);
    });

    it("throws when output is not set in builder function", () => {
      expect(() =>
        adapter.capture((_builder) => {
          // forgot to call setOutput
        }),
      ).toThrow("Trace output is required");
    });
  });

  describe("createBuilder()", () => {
    it("returns a TraceBuilder instance", () => {
      const builder = adapter.createBuilder();
      expect(builder).toBeInstanceOf(TraceBuilder);
    });

    it("builder has correct agent_id set", () => {
      const builder = adapter.createBuilder();
      const trace = builder.setOutput({ done: true }).build();
      expect(trace.agent_id).toBe("manual-agent");
    });

    it("supports full builder API", () => {
      const builder = adapter.createBuilder();
      builder
        .setInput({ query: "test" })
        .addLlmCall("model", { result: { text: "response" } })
        .setMetadata({ total_tokens: 50 })
        .setOutput({ message: "response" });

      const trace = builder.build();
      expect(trace.input).toEqual({ query: "test" });
      expect(trace.steps).toHaveLength(1);
      expect(trace.metadata?.total_tokens).toBe(50);
      expect(trace.output.message).toBe("response");
    });
  });

  describe("without agentId", () => {
    it("works without agentId in capture()", () => {
      const noIdAdapter = new ManualAdapter();
      const trace = noIdAdapter.capture((builder) => {
        builder.setOutput({ done: true });
      });
      expect(trace.agent_id).toBeUndefined();
    });

    it("works without agentId in createBuilder()", () => {
      const noIdAdapter = new ManualAdapter();
      const builder = noIdAdapter.createBuilder();
      const trace = builder.setOutput({ done: true }).build();
      expect(trace.agent_id).toBeUndefined();
    });
  });
});
