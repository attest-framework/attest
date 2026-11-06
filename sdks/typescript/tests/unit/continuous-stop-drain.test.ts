import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ContinuousEvalRunner } from "../../packages/core/src/continuous.js";
import type { AttestClient } from "../../packages/core/src/client.js";
import type { Assertion, DriftReport, EvaluateBatchResult, Trace } from "../../packages/core/src/proto/types.js";

function makeTrace(id: string): Trace {
  return { trace_id: id, output: { message: "x" }, steps: [] };
}

const assertion = {
  assertion_id: "",
  type: "content",
  spec: { target: "output.message", check: "contains", value: "x" },
} as unknown as Assertion;

function failingBatch(trace: Trace, assertions: readonly Assertion[]): EvaluateBatchResult {
  return {
    results: assertions.map((a) => ({
      assertion_id: a.assertion_id,
      status: "hard_fail",
      score: 0,
      explanation: trace.trace_id,
    })),
    total_cost: 0,
    total_duration_ms: 0,
    usage: { model_calls: 0, input_tokens: 0, output_tokens: 0 },
  } as unknown as EvaluateBatchResult;
}

const servers: Server[] = [];

async function startReceiver(): Promise<{ url: string; bodies: Record<string, unknown>[] }> {
  const bodies: Record<string, unknown>[] = [];
  const server = createServer((req, res) => {
    let raw = "";
    req.on("data", (chunk: Buffer) => (raw += chunk.toString()));
    req.on("end", () => {
      bodies.push(JSON.parse(raw) as Record<string, unknown>);
      res.statusCode = 200;
      res.end("{}");
    });
  });
  servers.push(server);
  await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
  return { url: `http://127.0.0.1:${(server.address() as AddressInfo).port}`, bodies };
}

afterEach(async () => {
  vi.restoreAllMocks();
  await Promise.all(servers.splice(0).map((s) => new Promise<void>((done) => s.close(() => done()))));
});

describe("ContinuousEvalRunner.stop drains queued traces", () => {
  it("evaluates and alerts every queued trace before stop() resolves", async () => {
    const receiver = await startReceiver();
    const client = {
      evaluateBatch: vi.fn(async (trace: Trace, assertions: readonly Assertion[]) => failingBatch(trace, assertions)),
      queryDrift: vi.fn(async (id: string) => ({ assertion_id: id, status: "stable" }) as unknown as DriftReport),
    } as unknown as AttestClient;
    const runner = new ContinuousEvalRunner(client, [assertion], { name: "drain", alertWebhook: receiver.url });

    for (const id of ["t1", "t2", "t3", "t4"]) runner.submit(makeTrace(id));
    runner.start();
    await runner.stop();

    expect(client.evaluateBatch).toHaveBeenCalledTimes(4);
    expect(receiver.bodies.map((b) => b["trace_id"]).sort()).toEqual(["t1", "t2", "t3", "t4"]);
    expect(runner.queueLength).toBe(0);
  });

  it("gives up at drainTimeoutMs, reports the pending work and drops the rest", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const errors: unknown[] = [];
    const client = {
      evaluateBatch: vi.fn(
        (trace: Trace, assertions: readonly Assertion[]) =>
          new Promise<EvaluateBatchResult>((resolve) => setTimeout(() => resolve(failingBatch(trace, assertions)), 80)),
      ),
      queryDrift: vi.fn(),
    } as unknown as AttestClient;
    const runner = new ContinuousEvalRunner(client, [assertion], {
      name: "drain-timeout",
      drainTimeoutMs: 100,
      onError: (e) => errors.push(e),
    });

    for (const id of ["t1", "t2", "t3", "t4", "t5"]) runner.submit(makeTrace(id));
    await runner.stop();

    expect(errors.some((e) => String(e).includes("stop() gave up after 100 ms"))).toBe(true);
    expect(vi.mocked(client.evaluateBatch).mock.calls.length).toBeLessThan(5);
  });

  it("discards the whole remaining queue at the timeout, lets the in-flight trace finish, and a restart evaluates nothing old", async () => {
    vi.useFakeTimers();
    try {
      vi.spyOn(console, "error").mockImplementation(() => undefined);
      const errors: unknown[] = [];
      const evaluated: string[] = [];
      const client = {
        evaluateBatch: vi.fn(
          (trace: Trace, assertions: readonly Assertion[]) =>
            new Promise<EvaluateBatchResult>((resolve) =>
              setTimeout(() => {
                evaluated.push(trace.trace_id);
                resolve(failingBatch(trace, assertions));
              }, 80),
            ),
        ),
        queryDrift: vi.fn(),
      } as unknown as AttestClient;
      const runner = new ContinuousEvalRunner(client, [assertion], {
        name: "drain-discard",
        drainTimeoutMs: 20,
        onError: (e) => errors.push(e),
      });

      for (const id of ["t1", "t2", "t3", "t4"]) runner.submit(makeTrace(id));
      const stopped = runner.stop();
      await vi.advanceTimersByTimeAsync(20);
      await stopped;

      expect(errors).toHaveLength(1);
      expect(String(errors[0])).toContain("discarded 3 queued trace(s)");
      expect(runner.queueLength).toBe(0);

      await vi.advanceTimersByTimeAsync(500);
      expect(evaluated).toEqual(["t1"]);
      expect(runner.queueLength).toBe(0);

      runner.start();
      await vi.advanceTimersByTimeAsync(500);
      const restarted = runner.stop();
      await vi.advanceTimersByTimeAsync(500);
      await restarted;
      expect(evaluated).toEqual(["t1"]);
    } finally {
      vi.useRealTimers();
    }
  });
});
