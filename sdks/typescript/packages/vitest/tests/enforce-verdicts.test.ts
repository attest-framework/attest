import { describe, expect, it, vi } from "vitest";
import type { TestContext } from "vitest";
import { AgentResult, RepeatResult, ScenarioResult, scenario } from "@attest-ai/core";
import type { AssertionResult, Trace } from "@attest-ai/core";
import { enforceOutcome } from "../src/fixtures.js";

function verdict(status: string, explanation = "boom"): AgentResult {
  const row = {
    assertion_id: "a",
    status,
    score: 0,
    explanation,
    ...(status === "not_evaluated" ? { reason: "simulation" } : {}),
  } as AssertionResult;
  return new AgentResult({ trace_id: "t", output: {} } as unknown as Trace, [row]);
}

function fakeContext(): { context: TestContext; skip: ReturnType<typeof vi.fn> } {
  const skip = vi.fn();
  return { context: { skip } as unknown as TestContext, skip };
}

const config = { mockTools: {}, faultErrorRate: 0, faultLatencyJitterMs: 0, maxTurns: 1 };

describe("enforceOutcome with aggregate verdicts", () => {
  it("throws the turn diagnostics for a failing ScenarioResult", () => {
    const result = new ScenarioResult(config, [verdict("pass"), verdict("hard_fail", "bad refund")]);

    expect(() => enforceOutcome(result, false)).toThrow(/turn 2\/2[\s\S]*bad refund/);
  });

  it("skips with the not-evaluated reasons for a skipped ScenarioResult", () => {
    const { context, skip } = fakeContext();
    const result = new ScenarioResult(config, [verdict("pass"), verdict("not_evaluated")]);

    expect(enforceOutcome(result, false, context)).toBe(result);
    expect(skip).toHaveBeenCalledWith("not evaluated: simulation");
  });

  it("turns a skipped ScenarioResult into a failure when strict", () => {
    const result = new ScenarioResult(config, [verdict("not_evaluated")]);

    expect(() => enforceOutcome(result, true)).toThrow(/strict: not evaluated: simulation/);
  });

  it("enforces a RepeatResult: fail, skip, and pass", () => {
    const { context, skip } = fakeContext();

    expect(() =>
      enforceOutcome(new RepeatResult([verdict("pass"), verdict("hard_fail")]), false),
    ).toThrow(/trial 2\/2/);
    enforceOutcome(new RepeatResult([verdict("pass"), verdict("not_evaluated")]), false, context);
    expect(skip).toHaveBeenCalledWith("not evaluated: simulation");

    const ok = new RepeatResult([verdict("pass"), verdict("soft_fail")]);
    expect(enforceOutcome(ok, false)).toBe(ok);
  });

  it("enforces a result produced by scenario()", async () => {
    const run = scenario({ maxTurns: 2 }, () => verdict("hard_fail", "scenario broke"));

    await expect(run().then((r) => enforceOutcome(r, false))).rejects.toThrow(/scenario broke/);
  });

  it("still enforces a plain AgentResult", () => {
    expect(() => enforceOutcome(verdict("hard_fail", "plain"), false)).toThrow(/plain/);
  });
});
