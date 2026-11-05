// Run by transport-routing.test.ts in a vitest subprocess against the real engine.
import { it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import { evaluate } from "../../src/index.js";

const trace = new TraceBuilder("refund-agent")
  .setOutput({ message: "Your refund has been issued." })
  .build();

it.concurrent("concurrent passes", async (context) => {
  await evaluate(attestExpect(trace).outputContains("refund"), { context });
});

it.concurrent("concurrent fails", async (context) => {
  await evaluate(attestExpect(trace).outputContains("absent-concurrent"), { context });
});
