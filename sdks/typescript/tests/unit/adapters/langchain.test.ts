import { describe, it, expect } from "vitest";
import { RunnableLambda } from "@langchain/core/runnables";
import { DynamicStructuredTool, DynamicTool, StructuredTool } from "@langchain/core/tools";
import { FakeListChatModel } from "@langchain/core/utils/testing";
import { LangChainAdapter } from "../../../packages/core/src/adapters/langchain.js";
import { STEP_LLM_CALL, STEP_TOOL_CALL } from "../../../packages/core/src/proto/constants.js";

const RUN = "00000000-0000-4000-8000-000000000001";
const CHILD = "00000000-0000-4000-8000-000000000002";
const OTHER = "00000000-0000-4000-8000-000000000003";

describe("LangChainAdapter", () => {
  it("captures a real langchainjs run passed in callbacks", async () => {
    const adapter = new LangChainAdapter("lc-agent");
    const lookup = new DynamicTool({
      name: "lookup_order",
      description: "Look up an order.",
      func: async () => "shipped",
    });
    const model = new FakeListChatModel({ responses: ["ORD-1 has shipped."] });
    const chain = RunnableLambda.from(async (question: string, config) => {
      const status = await lookup.invoke("ORD-1", config);
      const answer = await model.invoke(`${question} ${status}`, config);
      return { output: String(answer.content) };
    });

    await chain.invoke("Where is ORD-1?", { callbacks: [adapter] });
    const trace = adapter.buildTrace();

    expect(trace.agent_id).toBe("lc-agent");
    expect(trace.input).toEqual({ message: "Where is ORD-1?" });
    expect(trace.output.message).toBe("ORD-1 has shipped.");
    expect(trace.steps.map((s) => [s.type, s.name])).toEqual([
      [STEP_TOOL_CALL, "lookup_order"],
      [STEP_LLM_CALL, "llm"],
    ]);
    expect(trace.steps[0].args).toEqual({ input: "ORD-1" });
    expect(trace.steps[0].result).toEqual({ output: "shipped" });
    expect(trace.steps[1].result?.completion).toBe("ORD-1 has shipped.");
  });

  it("names a StructuredTool subclass after its tool name, not its class", async () => {
    class MyLookup extends StructuredTool {
      name = "lookup_order";
      description = "Look up an order.";
      schema = {
        type: "object" as const,
        properties: { order_id: { type: "string" } },
        required: ["order_id"],
      };
      async _call(): Promise<string> {
        return "shipped";
      }
    }
    const adapter = new LangChainAdapter();

    await new MyLookup().invoke(
      { type: "tool_call", id: "call_1", name: "lookup_order", args: { order_id: "ORD-1" } },
      { callbacks: [adapter] },
    );
    const step = adapter.buildTrace().steps[0];

    expect(step.name).toBe("lookup_order");
    expect(step.result).toEqual({ output: "shipped" });
  });

  it("keeps the tool name when a tool returns a plain object with its own name field", async () => {
    const getCustomer = new DynamicStructuredTool({
      name: "get_customer",
      description: "Get a customer.",
      schema: { type: "object" as const, properties: { id: { type: "string" } }, required: ["id"] },
      func: async ({ id }: { id: string }) => ({ name: "Alice", id }),
    });
    const adapter = new LangChainAdapter();

    await getCustomer.invoke({ id: "C-1" }, { callbacks: [adapter] });

    expect(adapter.buildTrace().steps[0].name).toBe("get_customer");
  });

  it("keys interleaved tool runs by runId", () => {
    const adapter = new LangChainAdapter();

    adapter.handleToolStart({}, '{"a":1}', CHILD, RUN, undefined, undefined, "first");
    adapter.handleToolStart({}, '{"b":2}', OTHER, RUN, undefined, undefined, "second");
    adapter.handleToolEnd("two", OTHER);
    adapter.handleToolEnd("one", CHILD);
    const steps = adapter.buildTrace().steps;

    expect(steps.map((s) => [s.name, s.args, s.result])).toEqual([
      ["second", { b: 2 }, { output: "two" }],
      ["first", { a: 1 }, { output: "one" }],
    ]);
  });

  it("reads a tool's name from the serialized id when the run has no name", () => {
    const adapter = new LangChainAdapter();

    adapter.handleToolStart({ id: ["langchain", "tools", "Calculator"] }, "2+2", CHILD, RUN);
    adapter.handleToolEnd("4", CHILD);
    const step = adapter.buildTrace().steps[0];

    expect(step.name).toBe("Calculator");
    expect(step.args).toEqual({ input: "2+2" });
  });

  it("records a tool error as the step result", () => {
    const adapter = new LangChainAdapter();

    adapter.handleToolStart({}, "{}", CHILD, RUN, undefined, undefined, "lookup");
    adapter.handleToolError(new Error("not found"), CHILD);

    expect(adapter.buildTrace().steps[0].result).toEqual({ error: "not found" });
  });

  it("reads token usage from the message and totals it", () => {
    const adapter = new LangChainAdapter();
    const message = { usage_metadata: { input_tokens: 100, output_tokens: 50, total_tokens: 150 } };

    adapter.handleChatModelStart({}, [[]], CHILD, RUN, { invocation_params: { model: "gpt-4.1" } });
    adapter.handleLLMEnd({ generations: [[{ text: "Hello", message }]] }, CHILD);
    const trace = adapter.buildTrace();

    expect(trace.metadata?.total_tokens).toBe(150);
    expect(trace.metadata?.model).toBe("gpt-4.1");
    expect(trace.steps[0].result).toEqual({
      completion: "Hello",
      tokens: 150,
      input_tokens: 100,
      output_tokens: 50,
    });
  });

  it("falls back to the provider's llmOutput token usage", () => {
    const adapter = new LangChainAdapter();

    adapter.handleLLMEnd(
      {
        generations: [[{ text: "Hi" }]],
        llmOutput: { tokenUsage: { promptTokens: 7, completionTokens: 3 }, model_name: "m" },
      },
      CHILD,
    );

    expect(adapter.buildTrace().metadata).toMatchObject({ total_tokens: 10, model: "m" });
  });

  it("takes the answer from the last LangGraph message", () => {
    const adapter = new LangChainAdapter();

    adapter.handleChainStart({}, { messages: [{ content: "hi" }] }, RUN);
    adapter.handleChainEnd({ messages: [{ content: "first" }, { content: [{ type: "text", text: "last" }] }] }, RUN);
    const trace = adapter.buildTrace();

    expect(trace.input).toEqual({ message: "hi" });
    expect(trace.output.message).toBe("last");
  });

  it("ignores nested chain starts and ends", () => {
    const adapter = new LangChainAdapter();

    adapter.handleChainStart({}, { input: "outer" }, RUN);
    adapter.handleChainStart({}, { input: "inner" }, CHILD, RUN);
    adapter.handleChainEnd({ output: "inner out" }, CHILD, RUN);
    adapter.handleChainEnd({ output: "outer out" }, RUN);
    const trace = adapter.buildTrace();

    expect(trace.input).toEqual({ message: "outer" });
    expect(trace.output.message).toBe("outer out");
  });

  it("clears the capture on reset", () => {
    const adapter = new LangChainAdapter();
    adapter.handleLLMEnd({ generations: [[{ text: "x" }]] }, CHILD);

    adapter.reset();

    expect(adapter.buildTrace().steps).toEqual([]);
  });

  it("is shared, not cloned, by the callback manager", () => {
    const adapter = new LangChainAdapter();

    expect(adapter.copy()).toBe(adapter);
    expect(adapter.awaitHandlers).toBe(true);
  });
});
