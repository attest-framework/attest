import { describe, it, expect, vi, afterEach } from "vitest";
import { Sampler, ContinuousEvalRunner, AlertDispatcher } from "../../packages/core/src/continuous.js";
import { assertionIdOf } from "../../packages/core/src/assertion-id.js";
import type { AttestClient } from "../../packages/core/src/client.js";
import type { Assertion, Trace, EvaluateBatchResult } from "../../packages/core/src/proto/types.js";

function makeTrace(id: string): Trace {
  return {
    trace_id: id,
    output: { message: "test" },
    steps: [],
  };
}

function makeMockClient(): AttestClient {
  return {
    evaluateBatch: vi.fn<
      [Trace, readonly Assertion[], { timeout?: number }?],
      Promise<EvaluateBatchResult>
    >().mockResolvedValue({
      results: [],
      total_cost: 0,
      total_duration_ms: 0,
      usage: {
        model_calls: 0,
        judge_calls: 0,
        input_tokens: 0,
        output_tokens: 0,
        cached_tokens: 0,
        cost_usd: 0,
        retry_cost_usd: 0,
        unpriced_calls: 0,
        budget_refused: 0,
      },
    }),
  } as unknown as AttestClient;
}

describe("Sampler", () => {
  it("rejects invalid rates", () => {
    expect(() => new Sampler(-0.1)).toThrow();
    expect(() => new Sampler(1.1)).toThrow();
  });

  it("rate 1.0 always samples", () => {
    const s = new Sampler(1.0);
    for (let i = 0; i < 10; i++) {
      expect(s.shouldSample()).toBe(true);
    }
  });

  it("rate 0.0 never samples", () => {
    const s = new Sampler(0.0);
    for (let i = 0; i < 10; i++) {
      expect(s.shouldSample()).toBe(false);
    }
  });
});

describe("AlertDispatcher", () => {
  it("emits alert events", () => {
    const dispatcher = new AlertDispatcher();
    const alerts: unknown[] = [];
    dispatcher.on("alert", (a) => alerts.push(a));

    dispatcher.dispatch({ drift_type: "regression", score: 0.3, trace_id: "trc_1" });
    expect(alerts).toHaveLength(1);
  });
});

describe("ContinuousEvalRunner", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("submits traces to the queue", () => {
    const client = makeMockClient();
    const runner = new ContinuousEvalRunner(client, [], { name: "prod-monitor" });

    expect(runner.submit(makeTrace("trc_1"))).toBe(true);
    expect(runner.queueLength).toBe(1);
  });

  it("rejects when queue is full", () => {
    const client = makeMockClient();
    const runner = new ContinuousEvalRunner(client, [], { name: "prod-monitor", maxSize: 2 });

    expect(runner.submit(makeTrace("trc_1"))).toBe(true);
    expect(runner.submit(makeTrace("trc_2"))).toBe(true);
    expect(runner.submit(makeTrace("trc_3"))).toBe(false);
  });

  it("start and stop toggle isRunning", () => {
    const client = makeMockClient();
    const runner = new ContinuousEvalRunner(client, [], { name: "prod-monitor" });

    expect(runner.isRunning).toBe(false);
    runner.start();
    expect(runner.isRunning).toBe(true);
    runner.stop();
    expect(runner.isRunning).toBe(false);
  });

  it("evaluateTrace returns null when not sampled", async () => {
    const client = makeMockClient();
    const runner = new ContinuousEvalRunner(client, [], { name: "prod-monitor", sampleRate: 0.0 });

    const result = await runner.evaluateTrace(makeTrace("trc_1"));
    expect(result).toBeNull();
  });

  it("evaluateTrace calls client when sampled", async () => {
    const client = makeMockClient();
    const runner = new ContinuousEvalRunner(client, [], { name: "prod-monitor", sampleRate: 1.0 });

    const result = await runner.evaluateTrace(makeTrace("trc_1"));
    expect(result).not.toBeNull();
    expect(client.evaluateBatch).toHaveBeenCalledTimes(1);
  });

  it("scopes assertion ids to the runner name and sends scope_id", async () => {
    const client = makeMockClient();
    const ok = { assertion_id: "x", type: "content", spec: { target: "output.message", check: "contains", value: "ok" } };
    const runner = new ContinuousEvalRunner(client, [ok, ok], { name: "prod-monitor" });
    await runner.evaluateTrace(makeTrace("trc_1"));
    const sent = vi.mocked(client.evaluateBatch).mock.calls[0]![1];
    expect(sent.map((a) => a.scope_id)).toEqual(["prod-monitor", "prod-monitor"]);
    expect(sent[0]!.assertion_id).toBe(assertionIdOf("prod-monitor", "content", ok.spec, 0));
    expect(sent[1]!.assertion_id).toBe(assertionIdOf("prod-monitor", "content", ok.spec, 1));
  });

  it("requires a runner name", () => {
    const client = makeMockClient();
    expect(() => new ContinuousEvalRunner(client, [], {} as { name: string })).toThrow(/options\.name/);
  });
});
