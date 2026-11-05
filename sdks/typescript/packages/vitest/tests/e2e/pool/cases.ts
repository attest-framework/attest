// Shared by the pool fixtures: one passing and two failing Attest evaluations per file.
import { it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import { evaluate } from "../../../src/index.js";

const trace = new TraceBuilder("refund-agent")
  .setOutput({ message: "Your refund has been issued." })
  .build();

export function registerCases(prefix: string): void {
  it(`${prefix} passes`, async (context) => {
    await evaluate(attestExpect(trace).outputContains("refund"), { context });
  });
  it(`${prefix} fails one`, async (context) => {
    await evaluate(attestExpect(trace).outputContains("store credit"), { context });
  });
  it(`${prefix} fails two`, async (context) => {
    await evaluate(attestExpect(trace).outputContains("gift card"), { context });
  });
}
