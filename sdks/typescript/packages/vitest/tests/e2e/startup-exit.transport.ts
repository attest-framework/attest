// Run by transport-routing.test.ts with ATTEST_ENGINE_PATH set to an engine that exits during startup.
import { it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import { evaluate } from "../../src/index.js";

const trace = new TraceBuilder("refund-agent").setOutput({ message: "ok" }).build();

it("startup exit", async (context) => {
  await evaluate(attestExpect(trace).outputContains("ok"), { context });
});
