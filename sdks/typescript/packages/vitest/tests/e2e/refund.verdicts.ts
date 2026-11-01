// Run by honest-verdicts.test.ts in a vitest subprocess against the real engine.
import { it } from "vitest";
import { Agent, attestExpect, type TraceBuilder } from "@attest-ai/core";
import { evaluate } from "../../src/index.js";

const refundAgent = new Agent("refund-agent", (builder: TraceBuilder, args: Record<string, unknown>) => {
  const loops = Number(args.loops ?? 1);
  for (let i = 0; i < loops; i++) {
    builder.addToolCall("lookup_order", {
      args: { order_id: args.order_id },
      result: { status: "delivered", amount: 89.99 },
    });
  }
  builder.addToolCall("process_refund", {
    args: { order_id: args.order_id },
    result: { refund_id: "RFD-001" },
  });
  builder.setMetadata({ cost_usd: 0.005, total_tokens: 150, latency_ms: 1200 });
  return {
    message: "Your refund of $89.99 has been processed.",
    structured: { refund_id: "RFD-001" },
  };
});

it("loop violated", async () => {
  const result = refundAgent.run({ order_id: "ORD-1", loops: 3 });
  await evaluate(
    attestExpect(result)
      .outputMatchesSchema({ type: "object", required: ["refund_id"] })
      .costUnder(0.05)
      .noToolLoops("lookup_order", 1)
      .outputContains("refund")
      .passesJudge("Is the response helpful and accurate?"),
  );
});

it("constraint violated", async () => {
  await evaluate(attestExpect(refundAgent.run({ order_id: "ORD-2" })).costUnder(0.001));
});

it("content violated", async () => {
  await evaluate(attestExpect(refundAgent.run({ order_id: "ORD-3" })).outputContains("apology"));
});

it("judge only", async () => {
  await evaluate(attestExpect(refundAgent.run({ order_id: "ORD-4" })).passesJudge("Helpful?"));
});

it.concurrent("judge only with context", async (context) => {
  await evaluate(attestExpect(refundAgent.run({ order_id: "ORD-5" })).passesJudge("Helpful?"), {
    context,
  });
});

it.concurrent("concurrent without context", async () => {
  await evaluate(attestExpect(refundAgent.run({ order_id: "ORD-6" })).passesJudge("Helpful?"));
});
