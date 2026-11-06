// Run by packaged-subpaths.test.ts in a vitest subprocess against the real engine and a fake judge.
import { it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import { evaluate } from "../../src/index.js";

const trace = new TraceBuilder("refund-agent").setOutput({ message: "Your refund has been issued." }).build();

it("judged", async (context) => {
  await evaluate(attestExpect(trace).passesJudge("Is it polite?"), { context });
});
