// Run by checked.test.ts in a vitest subprocess against the real engine.
import { it } from "vitest";
import { attestExpect, TraceBuilder, type AttestPlugin } from "@attest-ai/core";
import { attestPlugins, evaluate } from "../../src/index.js";

const trace = new TraceBuilder("refund-agent")
  .setOutput({ message: "ok, your refund has been issued." })
  .build();

const plugin = (name: string, status: string): AttestPlugin => ({
  name,
  pluginType: "assertion",
  execute: () => ({ status, score: status === "pass" ? 1 : 0, explanation: `${name} says ${status}` }),
});
attestPlugins.register("always-pass", plugin("always-pass", "pass"));
attestPlugins.register("always-fail", plugin("always-fail", "hard_fail"));

it("never evaluated", () => {
  attestExpect(trace).outputContains("x");
});

it("two never evaluated", () => {
  attestExpect(trace).outputContains("x");
  attestExpect(trace).outputContains("y");
});

it("empty chain", async (context) => {
  await evaluate(attestExpect(trace), { context });
});

it("evaluated chain", async (context) => {
  await evaluate(attestExpect(trace).outputContains("ok"), { context });
});

it("duplicate", async (context) => {
  await evaluate(attestExpect(trace).outputContains("ok").outputContains("ok"), { context });
});

it("budget breach with hard fail", async (context) => {
  await evaluate(
    attestExpect(trace)
      .outputContains("zzz1", { soft: true })
      .outputContains("zzz2", { soft: true })
      .outputContains("qqq"),
    { context },
  );
});

it("plugin pass beside judge", async (context) => {
  await evaluate(attestExpect(trace).plugin("always-pass").passesJudge("Is it polite?"), { context });
});

it("plugin fail beside judge", async (context) => {
  await evaluate(attestExpect(trace).plugin("always-fail").passesJudge("Is it polite?"), { context });
});
