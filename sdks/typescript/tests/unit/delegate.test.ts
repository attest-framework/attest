import { describe, it, expect } from "vitest";
import { delegate } from "../../packages/core/src/delegate.js";
import { TraceBuilder } from "../../packages/core/src/trace.js";
import { activeBuilder } from "../../packages/core/src/context.js";
import { STEP_AGENT_CALL } from "../../packages/core/src/proto/constants.js";

describe("delegate()", () => {
  it("throws when called outside an agent context", async () => {
    await expect(
      delegate("sub-agent", async (_child: TraceBuilder) => {}),
    ).rejects.toThrow("delegate() must be used within an Agent.run() context");
  });

  it("sets parent_trace_id on child trace", async () => {
    const parent = new TraceBuilder("parent-agent");
    let capturedChild: TraceBuilder | undefined;

    await activeBuilder.run(parent, async () => {
      await delegate("child-agent", async (child: TraceBuilder) => {
        capturedChild = child;
        child.setOutput({ done: true });
      });
    });

    const builtParent = parent.setOutput({ delegated: true }).build();
    const parentTraceId = builtParent.trace_id;

    // The child trace is embedded in the parent's agent_call step
    const agentCallStep = builtParent.steps.find((s) => s.type === STEP_AGENT_CALL);
    expect(agentCallStep).toBeDefined();
    expect(agentCallStep?.name).toBe("child-agent");

    const childTrace = agentCallStep?.sub_trace;
    expect(childTrace).toBeDefined();
    expect(childTrace?.parent_trace_id).toBe(parentTraceId);
  });

  it("adds an agent_call step with child trace result", async () => {
    const parent = new TraceBuilder("parent");

    await activeBuilder.run(parent, async () => {
      await delegate("worker", async (child: TraceBuilder) => {
        child.setOutput({ result: "computed" });
      });
    });

    parent.setOutput({ done: true });
    const trace = parent.build();

    expect(trace.steps).toHaveLength(1);
    const step = trace.steps[0];
    expect(step.type).toBe(STEP_AGENT_CALL);
    expect(step.name).toBe("worker");
    expect(step.result).toEqual({ result: "computed" });
    expect(step.sub_trace?.output).toEqual({ result: "computed" });
  });

  it("child trace has correct agent_id", async () => {
    const parent = new TraceBuilder("parent");

    await activeBuilder.run(parent, async () => {
      await delegate("specialist", async (child: TraceBuilder) => {
        child.setOutput({ answer: 42 });
      });
    });

    parent.setOutput({ done: true });
    const trace = parent.build();
    const childTrace = trace.steps[0].sub_trace;

    expect(childTrace?.agent_id).toBe("specialist");
  });

  it("child trace records steps added within fn", async () => {
    const parent = new TraceBuilder("parent");

    await activeBuilder.run(parent, async () => {
      await delegate("worker", async (child: TraceBuilder) => {
        child.addLlmCall("gpt-4", { result: { text: "hello" } });
        child.setOutput({ message: "hello" });
      });
    });

    parent.setOutput({ done: true });
    const trace = parent.build();
    const childTrace = trace.steps[0].sub_trace;

    expect(childTrace?.steps).toHaveLength(1);
    expect(childTrace?.steps[0].name).toBe("gpt-4");
  });
});
