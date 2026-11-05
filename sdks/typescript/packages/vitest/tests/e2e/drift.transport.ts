// Run by transport-routing.test.ts in a vitest subprocess against the real engine.
import { it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import { evaluate } from "../../src/index.js";

const trace = new TraceBuilder("refund-agent")
  .setOutput({ message: "Your refund has been issued." })
  .build();

// A failing assertion whose threshold is "dynamic" carries its drift data inside its own
// result. The chain DSL has no dynamic content check, so the spec is added through the
// chain's private add().
function dynamicContentFailure() {
  const chain = attestExpect(trace);
  chain["add"]("content", {
    target: "output.message",
    check: "contains",
    value: "never-in-output",
    case_sensitive: false,
    soft: false,
    threshold: "dynamic",
  });
  return chain;
}

it("A dynamic content hard-fail", async (context) => {
  await evaluate(dynamicContentFailure(), { context });
});

it("B passes", async (context) => {
  await evaluate(attestExpect(trace).outputContains("refund"), { context });
});

it("C fails", async (context) => {
  await evaluate(attestExpect(trace).outputContains("absent-in-c"), { context });
});
