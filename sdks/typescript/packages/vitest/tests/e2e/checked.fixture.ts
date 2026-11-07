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

it("chain in early beforeEach", () => {});

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

it("assertion appended after evaluation", async (context) => {
  const chain = attestExpect(trace).outputContains("ok");
  await evaluate(chain, { context });
  chain.outputContains("absent");
});

it("appended assertion re-evaluated", async (context) => {
  const chain = attestExpect(trace).outputContains("ok");
  await evaluate(chain, { context });
  chain.outputContains("refund");
  await evaluate(chain, { context });
});

it("assertion appended during evaluation", async (context) => {
  const chain = attestExpect(trace).outputContains("ok");
  const evaluating = evaluate(chain, { context });
  chain.outputContains("absent");
  await evaluating;
});

let retryAttempt = 0;
it("guard resets for retry", { retry: 1 }, async (context) => {
  const chain = attestExpect(trace).outputContains("ok");
  if (retryAttempt++ > 0) await evaluate(chain, { context });
});

it.concurrent("evaluates a chain in finish handler", (context) => {
  context.onTestFinished(async () => {
    await evaluate(attestExpect(trace).outputContains("ok"), { context });
  });
});

let lateRetryAttempt = 0;
it("late handler cannot exempt retry", { retry: 1 }, async (context) => {
  context.onTestFailed(async () => {
    await evaluate(attestExpect(trace).outputContains("ok"), { context });
  });
  if (lateRetryAttempt++ === 0) throw new Error("retry this attempt");
  attestExpect(trace).outputContains("absent");
});

let releaseConcurrent: () => void;
const concurrentReady = new Promise<void>((resolve) => { releaseConcurrent = resolve; });

it.concurrent("concurrent never evaluated", async () => {
  await concurrentReady;
  attestExpect(trace).outputContains("absent");
});

it.concurrent("concurrent evaluated chain", async (context) => {
  await evaluate(attestExpect(trace).outputContains("ok"), { context });
  releaseConcurrent();
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
