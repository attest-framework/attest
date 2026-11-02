/**
 * trials(), deterministic assertion IDs and per-test budgets, against a scripted
 * client (the engine boundary). Each test drives its own test task so the
 * recorded meta can be inspected.
 */
import { createHash } from "node:crypto";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { TestContext } from "vitest";
import {
  attestExpect,
  TraceBuilder,
  zeroUsage,
  type Assertion,
  type AttestClient,
  type BatchBudget,
  type Trace,
  type Usage,
} from "@attest-ai/core";
import { evaluate, inspect } from "../src/fixtures.js";
import type { AttestTaskMeta } from "../src/report.js";
import {
  deterministicAssertionId,
  remainingBudget,
  ROOT_ENV,
  testIdOf,
  validateBudget,
  type RunningTest,
} from "../src/test-state.js";
import { attestGlobalSetup } from "../src/plugin.js";
import { trials } from "../src/trials.js";

type Status = "pass" | "hard_fail" | "not_evaluated";

interface ScriptedCall {
  assertions: Assertion[];
  budget: BatchBudget | undefined;
}

/** A client whose n-th evaluate_batch answers every assertion with statuses[n]. */
function scriptedClient(statuses: Status[], usage: Usage = zeroUsage()) {
  const calls: ScriptedCall[] = [];
  const client = {
    evaluateBatch: vi.fn(
      async (_trace: Trace, assertions: readonly Assertion[], options?: { budget?: BatchBudget }) => {
        const status = statuses[calls.length];
        if (status === undefined) throw new Error(`unscripted evaluate_batch call ${calls.length + 1}`);
        calls.push({ assertions: [...assertions], budget: options?.budget });
        return {
          results: assertions.map((a) => ({
            assertion_id: a.assertion_id,
            status,
            score: status === "pass" ? 1 : 0,
            explanation: status,
            ...(status === "not_evaluated" ? { reason: "provider_not_configured" } : {}),
          })),
          total_cost: usage.cost_usd,
          total_duration_ms: 1,
          usage,
        };
      },
    ),
  };
  globalThis.__attest_client__ = client as unknown as AttestClient;
  return calls;
}

interface FakeTest {
  task: RunningTest;
  context: TestContext;
  skip: ReturnType<typeof vi.fn>;
  meta: () => AttestTaskMeta;
}

/** A test task under a suite, with a context whose skip() records instead of throwing. */
function fakeTest(name: string): FakeTest {
  const skip = vi.fn();
  const file = { name: "tests/agent.test.ts" };
  const suite = { name: "refunds", suite: file };
  const task = { name, suite, file, meta: {}, concurrent: false } as unknown as RunningTest;
  const context = { task, skip } as unknown as TestContext;
  (task as unknown as { context: TestContext }).context = context;
  return { task, context, skip, meta: () => task.meta.attest as AttestTaskMeta };
}

function chain(trace: Trace = new TraceBuilder("agent").setOutput({ message: "refund issued" }).build()) {
  return attestExpect(trace).outputContains("refund").requiredTools(["lookup_order"]);
}

function judged() {
  return attestExpect(new TraceBuilder("agent").setOutput({ message: "hi" }).build()).passesJudge("polite?");
}

afterEach(() => {
  globalThis.__attest_client__ = undefined;
  delete process.env["ATTEST_TRIALS"];
  delete process.env["ATTEST_STRICT"];
});

describe("deterministic assertion IDs", () => {
  it("are assert_ + 12 hex chars of sha256(test_id#ordinal)", () => {
    const expected = createHash("sha256").update("tests/a.test.ts > t#3").digest("hex").slice(0, 12);
    expect(deterministicAssertionId("tests/a.test.ts > t", 3)).toBe(`assert_${expected}`);
  });

  it("use the report's test_id: file > suites > name", () => {
    expect(testIdOf(fakeTest("issues refunds").task)).toBe("tests/agent.test.ts > refunds > issues refunds");
  });

  it("hash the file path relative to the Vitest root AttestReporter publishes", () => {
    const t = fakeTest("issues refunds");
    // A project rooted at packages/api: Vitest names the file relative to the
    // project, the report relative to the Vitest root.
    Object.assign(t.task.file, { name: "tests/agent.test.ts", filepath: "/repo/packages/api/tests/agent.test.ts" });
    process.env[ROOT_ENV] = "/repo";
    try {
      expect(testIdOf(t.task)).toBe("packages/api/tests/agent.test.ts > refunds > issues refunds");
    } finally {
      delete process.env[ROOT_ENV];
    }
  });

  it("match the running test's report test_id", (context) => {
    expect(testIdOf(context.task)).toBe(
      "tests/trials.test.ts > deterministic assertion IDs > match the running test's report test_id",
    );
  });

  it("count assertions across a test's evaluations and are random outside a test", async () => {
    const calls = scriptedClient(["pass", "pass", "pass"]);
    const t = fakeTest("ids");
    const testId = testIdOf(t.task);
    await inspect(chain(), { context: t.context });
    await inspect(chain(), { context: t.context });
    expect(calls.flatMap((c) => c.assertions.map((a) => a.assertion_id))).toEqual(
      [0, 1, 2, 3].map((i) => deterministicAssertionId(testId, i)),
    );

    const outside = chain();
    await useOutsideTest(() => inspect(outside));
    expect(calls[2]!.assertions.map((a) => a.assertion_id)).toEqual(outside.assertions.map((a) => a.assertion_id));
  });
});

/** Run fn with no running Vitest test, as in a module-level script. */
async function useOutsideTest<T>(fn: () => Promise<T>): Promise<T> {
  const vitest = await import("vitest");
  const spy = vi.spyOn(vitest.TestRunner, "getCurrentTest").mockReturnValue(undefined);
  try {
    return await fn();
  } finally {
    spy.mockRestore();
  }
}

describe("per-test budget", () => {
  const used = (judge_calls: number, cost_usd: number): Usage => ({ ...zeroUsage(), judge_calls, cost_usd });

  it("sends the remainder of judge calls and USD after attest and agent spend", () => {
    expect(remainingBudget({ maxJudgeCalls: 5, maxCostUsd: 1 }, used(2, 0.25), 0.5)).toEqual({
      max_judge_calls: 3,
      max_cost_usd: 0.25,
    });
  });

  it("floors the remainder at 0", () => {
    expect(remainingBudget({ maxJudgeCalls: 1, maxCostUsd: 0.1 }, used(4, 0.3), 0.2)).toEqual({
      max_judge_calls: 0,
      max_cost_usd: 0,
    });
  });

  it("sends only the limits that are set, and nothing for an empty budget", () => {
    expect(remainingBudget({ maxJudgeCalls: 2 }, used(1, 9), 9)).toEqual({ max_judge_calls: 1 });
    expect(remainingBudget({}, used(1, 1), 1)).toBeUndefined();
  });

  it("rejects negative, fractional and unknown limits", () => {
    expect(() => validateBudget({ maxJudgeCalls: -1 })).toThrow("maxJudgeCalls must be an integer >= 0");
    expect(() => validateBudget({ maxJudgeCalls: 1.5 })).toThrow("maxJudgeCalls must be an integer >= 0");
    expect(() => validateBudget({ maxCostUsd: Number.NaN })).toThrow("maxCostUsd must be a number >= 0");
    expect(() => validateBudget({ maxCost: 1 } as never)).toThrow('unknown option "maxCost"');
  });

  it("each evaluation in a test sends what its earlier evaluations left", async () => {
    const calls = scriptedClient(["pass", "pass"], { ...zeroUsage(), model_calls: 1, judge_calls: 1, cost_usd: 0.02 });
    const t = fakeTest("budget");
    const trace = new TraceBuilder("agent").setOutput({ message: "refund" }).setMetadata({ cost_usd: 0.01 }).build();
    const budget = { maxJudgeCalls: 3, maxCostUsd: 0.1 };
    await inspect(chain(trace), { context: t.context, testBudget: budget });
    await inspect(chain(trace), { context: t.context, testBudget: budget });
    // The trace's agent spend counts once; attest spend accumulates per call.
    expect(calls[0]!.budget).toEqual({ max_judge_calls: 3, max_cost_usd: 0.1 - 0.01 });
    expect(calls[1]!.budget!.max_judge_calls).toBe(2);
    expect(calls[1]!.budget!.max_cost_usd).toBeCloseTo(0.1 - 0.01 - 0.02, 12);
  });

  it("is omitted from the batch when the test sets none", async () => {
    const calls = scriptedClient(["pass"]);
    await inspect(chain(), { context: fakeTest("no budget").context });
    expect(calls[0]!.budget).toBeUndefined();
  });
});

describe("trials()", () => {
  it("records every trial's rows with trial = i under the same assertion IDs", async () => {
    const calls = scriptedClient(["pass", "hard_fail", "pass"]);
    const t = fakeTest("partial");
    await trials(async () => {
      await evaluate(chain()); // does not enforce the hard_fail of trial 2
    }, { n: 3, context: t.context, minPassRate: 0.5 });

    const testId = testIdOf(t.task);
    const ids = [deterministicAssertionId(testId, 0), deterministicAssertionId(testId, 1)];
    for (const call of calls) expect(call.assertions.map((a) => a.assertion_id)).toEqual(ids);
    expect(t.meta().results.map((r) => [r.assertion_id, r.trial, r.status])).toEqual([
      [ids[0], 1, "pass"],
      [ids[1], 1, "pass"],
      [ids[0], 2, "hard_fail"],
      [ids[1], 2, "hard_fail"],
      [ids[0], 3, "pass"],
      [ids[1], 3, "pass"],
    ]);
    expect(t.meta().trials).toEqual({
      requested: 3,
      run: 3,
      passed: 2,
      evaluated: 3,
      notEvaluated: 0,
      stoppedByBudget: false,
    });
    expect(t.skip).not.toHaveBeenCalled();
  });

  it("turns a thrown trial into hard_fail rows for the test's assertion IDs", async () => {
    scriptedClient(["pass", "pass"]);
    const t = fakeTest("thrown");
    await trials(async (i) => {
      if (i === 2) throw new TypeError("chat completion failed: HTTP 500");
      await evaluate(chain());
    }, { n: 3, context: t.context, minPassRate: 0.5 });

    const thrown = t.meta().results.filter((r) => r.trial === 2);
    expect(thrown).toHaveLength(2);
    for (const row of thrown) {
      expect(row).toMatchObject({
        status: "hard_fail",
        score: 0,
        explanation: "trial 2 raised TypeError: chat completion failed: HTTP 500",
        failure_class: "broken_code",
      });
    }
    expect(t.meta().trials).toMatchObject({ passed: 2, evaluated: 3, notEvaluated: 0 });
  });

  it("replaces the rows a trial evaluated before throwing, one hard_fail row per ID", async () => {
    scriptedClient(["pass", "pass", "pass"]);
    const t = fakeTest("thrown late");
    await trials(async (i) => {
      await evaluate(chain());
      if (i === 2) throw new Error("HTTP 500");
    }, { n: 3, context: t.context, minPassRate: 0.5 });

    const rows = t.meta().results;
    expect(rows).toHaveLength(6);
    expect(rows.filter((r) => r.trial === 2).map((r) => [r.status, r.explanation])).toEqual([
      ["hard_fail", "trial 2 raised Error: HTTP 500 (engine status before the error: pass)"],
      ["hard_fail", "trial 2 raised Error: HTTP 500 (engine status before the error: pass)"],
    ]);
    expect(t.meta().trials).toMatchObject({ passed: 2, evaluated: 3, notEvaluated: 0 });
  });

  it("skips with 'no assertions in N trials' when no trial evaluated or threw", async () => {
    scriptedClient([]);
    const t = fakeTest("empty");
    await trials(() => undefined, { n: 2, context: t.context });
    expect(t.skip).toHaveBeenCalledWith("not evaluated: no assertions in 2 trials");
    expect(t.meta().trials).toMatchObject({ passed: 0, evaluated: 0, notEvaluated: 2 });
  });

  it("fails with the failing trials' diagnostics when no trial passed", async () => {
    scriptedClient(["hard_fail"]);
    const t = fakeTest("none passed");
    const run = trials(async (i) => {
      if (i === 1) throw new Error("HTTP 500");
      await evaluate(chain());
    }, { n: 2, context: t.context });
    await expect(run).rejects.toThrow(
      /0\/2 trials passed \(pass rate 0\.00\) below min pass rate 1\.00\ntrial 1 raised Error: HTTP 500\ntrial 2:/,
    );
  });

  it("rethrows the first error when no trial evaluated an assertion", async () => {
    scriptedClient([]);
    const t = fakeTest("all thrown");
    const run = trials((i) => {
      throw new Error(`boom ${i}`);
    }, { n: 3, context: t.context });
    await expect(run).rejects.toThrow("boom 1");
  });

  it("skips with the summed reasons when no trial was evaluated", async () => {
    scriptedClient(["not_evaluated", "not_evaluated"]);
    const t = fakeTest("all skipped");
    await trials(async () => {
      await evaluate(judged());
    }, { n: 2, context: t.context });
    expect(t.skip).toHaveBeenCalledWith("not evaluated: provider_not_configured ×2");
    expect(t.meta().trials).toMatchObject({ passed: 0, evaluated: 0, notEvaluated: 2 });
  });

  it("fails instead of skipping under ATTEST_STRICT", async () => {
    process.env["ATTEST_STRICT"] = "1";
    scriptedClient(["not_evaluated", "not_evaluated"]);
    const t = fakeTest("strict");
    const run = trials(async () => {
      await evaluate(judged());
    }, { n: 2, context: t.context });
    await expect(run).rejects.toThrow("strict: not evaluated: provider_not_configured ×2");
    expect(t.skip).not.toHaveBeenCalled();
    // Strict fails the aggregate but a skipped trial still counts as not evaluated.
    expect(t.meta().trials).toMatchObject({ passed: 0, evaluated: 0, notEvaluated: 2 });
  });

  it("stops before a trial once attest cost plus agent spend reaches maxCostUsd", async () => {
    const calls = scriptedClient(["pass", "pass", "pass"], { ...zeroUsage(), model_calls: 1, judge_calls: 1, cost_usd: 0.02 });
    const t = fakeTest("budget stop");
    await trials(async () => {
      const trace = new TraceBuilder("agent").setOutput({ message: "refund" }).setMetadata({ cost_usd: 0.01 }).build();
      await evaluate(chain(trace));
    }, { n: 5, context: t.context, budget: { maxCostUsd: 0.05, maxJudgeCalls: 10 } });

    // After trial 1: 0.03 spent < 0.05; after trial 2: 0.06 >= 0.05.
    expect(calls).toHaveLength(2);
    expect(calls[1]!.budget!.max_judge_calls).toBe(9);
    expect(t.meta().trials).toMatchObject({ requested: 5, run: 2, stoppedByBudget: true, passed: 2 });
  });

  it("takes N from ATTEST_TRIALS and rejects an invalid N", async () => {
    process.env["ATTEST_TRIALS"] = "2";
    const calls = scriptedClient(["pass", "pass"]);
    await trials(async () => {
      await evaluate(chain());
    }, { context: fakeTest("env").context });
    expect(calls).toHaveLength(2);

    process.env["ATTEST_TRIALS"] = "two";
    await expect(trials(() => undefined, { context: fakeTest("bad").context })).rejects.toThrow(
      "N must be an integer >= 1, got ATTEST_TRIALS=two",
    );
    await expect(trials(() => undefined, { n: 0, context: fakeTest("zero").context })).rejects.toThrow(
      "N must be an integer >= 1, got n=0",
    );
  });

  it("with N = 1 runs the body once as a plain test: evaluate() enforces, rows carry no trial", async () => {
    scriptedClient(["hard_fail"]);
    const t = fakeTest("single");
    await expect(
      trials(async () => {
        await evaluate(chain());
      }, { n: 1, context: t.context }),
    ).rejects.toThrow();
    expect(t.meta().results.every((r) => r.trial === undefined)).toBe(true);
    expect(t.meta().trials).toBeUndefined();
  });
});

describe("trials() minimum pass rate", () => {
  /** n trials where the trials listed in `failing` hard_fail and the rest pass. */
  async function run(n: number, failing: number, options: { minPassRate?: number } = {}) {
    const statuses: Status[] = Array.from({ length: n }, (_, i) => (i < failing ? "hard_fail" : "pass"));
    scriptedClient(statuses);
    const t = fakeTest(`rate ${failing}/${n}`);
    await trials(async () => {
      await evaluate(chain());
    }, { n, context: t.context, ...options });
    return t;
  }

  afterEach(() => {
    delete process.env["ATTEST_MIN_PASS_RATE"];
  });

  it("defaults to 1.0: one failing trial in 20 fails the test", async () => {
    await expect(run(20, 1)).rejects.toThrow(
      "19/20 trials passed (pass rate 0.95) below min pass rate 1.00\ntrial 1:",
    );
  });

  it("passes at 18/20 and fails at 17/20 with minPassRate 0.9", async () => {
    const t = await run(20, 2, { minPassRate: 0.9 });
    expect(t.meta().trials).toMatchObject({ passed: 18, evaluated: 20 });
    await expect(run(20, 3, { minPassRate: 0.9 })).rejects.toThrow(
      "17/20 trials passed (pass rate 0.85) below min pass rate 0.90",
    );
  });

  it("takes the run's rate from ATTEST_MIN_PASS_RATE and lets trials() override it", async () => {
    process.env["ATTEST_MIN_PASS_RATE"] = "0.5";
    await run(4, 2);
    await expect(run(4, 2, { minPassRate: 0.75 })).rejects.toThrow("below min pass rate 0.75");
    process.env["ATTEST_MIN_PASS_RATE"] = "0.9";
    await run(4, 2, { minPassRate: 0.5 });
  });

  it("rejects rates outside [0, 1]", async () => {
    scriptedClient([]);
    const t = fakeTest("invalid");
    for (const minPassRate of [-0.1, 1.5, Number.NaN]) {
      await expect(trials(() => undefined, { n: 2, context: t.context, minPassRate })).rejects.toThrow(
        "minPassRate must be a number between 0 and 1",
      );
    }
    process.env["ATTEST_MIN_PASS_RATE"] = "most";
    await expect(trials(() => undefined, { n: 2, context: t.context })).rejects.toThrow(
      "minPassRate must be a number between 0 and 1, got ATTEST_MIN_PASS_RATE=most",
    );
    expect(() => attestGlobalSetup({ minPassRate: 2 })).toThrow("minPassRate must be a number between 0 and 1");
  });

  it("leaves not-evaluated trials out of the rate", async () => {
    scriptedClient(["pass", "not_evaluated", "not_evaluated"]);
    const t = fakeTest("partly judged");
    await trials(async (i) => {
      await evaluate(i === 1 ? chain() : judged());
    }, { n: 3, context: t.context });
    expect(t.meta().trials).toMatchObject({ passed: 1, evaluated: 1, notEvaluated: 2 });
    expect(t.skip).not.toHaveBeenCalled();
  });
});
