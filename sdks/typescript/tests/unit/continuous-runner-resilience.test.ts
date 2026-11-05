import { afterEach, describe, expect, it, vi } from "vitest";
import { ContinuousEvalRunner } from "../../packages/core/src/continuous.js";
import type { AttestClient } from "../../packages/core/src/client.js";
import type {
  Assertion,
  AssertionResult,
  DriftReport,
  EvaluateBatchResult,
  Trace,
} from "../../packages/core/src/proto/types.js";

function makeTrace(id: string): Trace {
  return { trace_id: id, output: { message: "x" }, steps: [] };
}

function spec(threshold: unknown): Assertion {
  return { assertion_id: "", type: "content", spec: { target: "output.message", check: "contains", value: "x", threshold } } as unknown as Assertion;
}

function batch(results: AssertionResult[]): EvaluateBatchResult {
  return {
    results,
    total_cost: 0,
    total_duration_ms: 0,
    usage: { model_calls: 0, input_tokens: 0, output_tokens: 0 },
  } as unknown as EvaluateBatchResult;
}

function result(assertionId: string, status: string, score: number): AssertionResult {
  return { assertion_id: assertionId, status, score, explanation: "" } as AssertionResult;
}

function report(assertionId: string, status: string): DriftReport {
  return { assertion_id: assertionId, mean: 0.9, stddev: 0.01, count: 10, latest_score: 0.5, deviation: 3, status };
}

const unhandled: unknown[] = [];
const onUnhandled = (reason: unknown): void => {
  unhandled.push(reason);
};

afterEach(() => {
  process.off("unhandledRejection", onUnhandled);
  unhandled.length = 0;
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("ContinuousEvalRunner observer isolation", () => {
  for (const mode of ["throws", "rejects"] as const) {
    it(`keeps the loop alive when onError ${mode}`, async () => {
      process.on("unhandledRejection", onUnhandled);
      vi.spyOn(console, "error").mockImplementation(() => undefined);
      const evaluateBatch = vi.fn().mockRejectedValue(new Error("engine down"));
      const client = { evaluateBatch, queryDrift: vi.fn() } as unknown as AttestClient;
      const onError = vi.fn((): void | Promise<void> => {
        if (mode === "throws") throw new Error("callback failed");
        return Promise.reject(new Error("callback failed"));
      });
      const runner = new ContinuousEvalRunner(client, [spec(0.8)], { name: "iso", onError });
      runner.submit(makeTrace("t1"));
      runner.submit(makeTrace("t2"));

      runner.start();
      const deadline = Date.now() + 3_000;
      while (evaluateBatch.mock.calls.length < 2 && Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, 10));
      }
      await runner.stop();
      await new Promise((r) => setTimeout(r, 20));

      expect(evaluateBatch).toHaveBeenCalledTimes(2);
      expect(runner.queueLength).toBe(0);
      expect(onError).toHaveBeenCalledTimes(2);
      expect(unhandled).toEqual([]);
    });
  }
});

describe("ContinuousEvalRunner serialization", () => {
  it("never interleaves evaluate and query_drift across concurrent evaluateTrace calls", async () => {
    const events: string[] = [];
    const client = {
      evaluateBatch: vi.fn(async (trace: Trace) => {
        events.push(`eval:${trace.trace_id}:start`);
        await new Promise((r) => setTimeout(r, 15));
        events.push(`eval:${trace.trace_id}:end`);
        return batch([result("a", "pass", 0.9)]);
      }),
      queryDrift: vi.fn(async () => {
        events.push("drift:start");
        await new Promise((r) => setTimeout(r, 15));
        events.push("drift:end");
        return report("a", "ok");
      }),
    } as unknown as AttestClient;
    const runner = new ContinuousEvalRunner(client, [spec(0.8)], { name: "serial" });

    await Promise.all([runner.evaluateTrace(makeTrace("t1")), runner.evaluateTrace(makeTrace("t2"))]);

    expect(events).toEqual([
      "eval:t1:start", "eval:t1:end", "drift:start", "drift:end",
      "eval:t2:start", "eval:t2:end", "drift:start", "drift:end",
    ]);
  });

  it("serializes a direct call against the background loop", async () => {
    let active = 0;
    let maxActive = 0;
    const track = async <T>(value: T): Promise<T> => {
      active++;
      maxActive = Math.max(maxActive, active);
      await new Promise((r) => setTimeout(r, 20));
      active--;
      return value;
    };
    const client = {
      evaluateBatch: vi.fn(() => track(batch([result("a", "pass", 0.9)]))),
      queryDrift: vi.fn(() => track(report("a", "ok"))),
    } as unknown as AttestClient;
    const runner = new ContinuousEvalRunner(client, [spec(0.8)], { name: "serial-loop" });
    runner.submit(makeTrace("loop"));
    runner.start();
    await new Promise((r) => setTimeout(r, 10));
    const direct = runner.evaluateTrace(makeTrace("direct"));
    await direct;
    await runner.stop();

    expect(maxActive).toBe(1);
  });
});

describe("ContinuousEvalRunner static threshold resolution", () => {
  const cases: [string, unknown, number][] = [
    ["numeric", 0.6, 0.6],
    ["zero", 0, 0.8],
    ["negative", -1, 0.8],
    ["missing", undefined, 0.8],
    ["dynamic", "dynamic", 0.8],
  ];
  for (const [label, threshold, expected] of cases) {
    it(`resolves a ${label} threshold to ${expected}`, async () => {
      const queryDrift = vi.fn(async () => report("a", "ok"));
      const client = {
        evaluateBatch: vi.fn(async (_t: Trace, assertions: readonly Assertion[]) =>
          batch([result(assertions[0]!.assertion_id, "pass", 0.9)]),
        ),
        queryDrift,
      } as unknown as AttestClient;
      const runner = new ContinuousEvalRunner(client, [spec(threshold)], { name: "thr" });

      await runner.evaluateTrace(makeTrace("t"));

      expect(queryDrift).toHaveBeenCalledWith(expect.any(String), undefined, { staticThreshold: expected });
    });
  }

  it("neither queries nor alerts rows that were not evaluated", async () => {
    const queryDrift = vi.fn();
    const client = {
      evaluateBatch: vi.fn(async () => batch([result("a", "not_evaluated", 0)])),
      queryDrift,
    } as unknown as AttestClient;
    const runner = new ContinuousEvalRunner(client, [spec(0.8)], { name: "ne" });

    await runner.evaluateTrace(makeTrace("t"));

    expect(queryDrift).not.toHaveBeenCalled();
  });
});
