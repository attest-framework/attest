// Run by attest-reporter.test.ts in a vitest subprocess against the real engine.
import { it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import { evaluate } from "../../src/index.js";

const trace = new TraceBuilder("refund-agent")
  .addToolCall("lookup_order", { args: { order_id: "ORD-1" } })
  .setOutput({ message: "Your refund has been issued." })
  .build();

it("passes", async (context) => {
  await evaluate(attestExpect(trace).outputContains("refund").requiredTools(["lookup_order"]), { context });
});

it("fails", async (context) => {
  await evaluate(attestExpect(trace).outputContains("store credit"), { context });
});

it("not evaluated", async (context) => {
  await evaluate(attestExpect(trace).passesJudge("Is it polite?"), { context });
});

it.skip("plain skip", () => {});

it("no attest", () => {});
