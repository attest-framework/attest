import { STEP_AGENT_CALL } from "./proto/constants.js";
import { TraceBuilder } from "./trace.js";
import { activeBuilder } from "./context.js";

export async function delegate(
  agentId: string,
  fn: (child: TraceBuilder) => Promise<void> | void,
): Promise<void> {
  const parent = activeBuilder.getStore();
  if (parent === undefined) {
    throw new Error(
      "delegate() must be used within an Agent.run() context. " +
      "No active TraceBuilder found.",
    );
  }

  const child = new TraceBuilder(agentId);
  child.setParentTraceId(parent.getTraceId());

  await activeBuilder.run(child, async () => {
    await fn(child);
  });

  const childTrace = child.build();

  parent.addStep({
    type: STEP_AGENT_CALL,
    name: agentId,
    args: undefined,
    result: childTrace.output,
    sub_trace: childTrace,
  });
}
