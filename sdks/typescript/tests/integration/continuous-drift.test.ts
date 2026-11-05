import { existsSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  AttestClient,
  ContinuousEvalRunner,
  EngineManager,
  TraceBuilder,
  attestExpect,
} from "../../packages/core/src/index.js";
import type { Trace } from "../../packages/core/src/proto/types.js";

const here = dirname(fileURLToPath(import.meta.url));
const enginePath = resolve(here, "../../../../bin/attest-engine");

const ENV_KEYS = [
  "ATTEST_JUDGE_PROVIDER",
  "ATTEST_JUDGE_BASE_URL",
  "ATTEST_JUDGE_API_KEY",
  "ATTEST_JUDGE_MODEL",
  "ATTEST_CACHE_DIR",
  "OPENAI_API_KEY",
] as const;

const ALERT_KEYS = [
  "assertion_id",
  "baseline_count",
  "baseline_mean",
  "result_status",
  "runner",
  "score",
  "static_threshold",
  "status",
  "trace_id",
];

interface Receiver {
  readonly url: string;
  readonly bodies: Record<string, unknown>[];
  close(): Promise<void>;
}

async function listen(server: Server): Promise<string> {
  await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
  return `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
}

function closeServer(server: Server): Promise<void> {
  return new Promise<void>((done) => server.close(() => done()));
}

async function startReceiver(status: number, delayMs = 0): Promise<Receiver> {
  const bodies: Record<string, unknown>[] = [];
  const server = createServer((req, res) => {
    let raw = "";
    req.on("data", (chunk: Buffer) => (raw += chunk.toString()));
    req.on("end", () => {
      bodies.push(JSON.parse(raw) as Record<string, unknown>);
      setTimeout(() => {
        res.statusCode = status;
        res.end("{}");
      }, delayMs);
    });
  });
  const url = await listen(server);
  return { url, bodies, close: () => closeServer(server) };
}

function until(predicate: () => boolean, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  return new Promise((resolveWait) => {
    const tick = (): void => {
      if (predicate()) resolveWait(true);
      else if (Date.now() > deadline) resolveWait(false);
      else setTimeout(tick, 20);
    };
    tick();
  });
}

function makeTrace(n: number): Trace {
  return new TraceBuilder("agent").setOutput({ message: `answer ${n}` }).build();
}

describe.skipIf(!existsSync(enginePath))("continuous-eval drift alerts (real engine, fake judge)", () => {
  const saved = Object.fromEntries(ENV_KEYS.map((k) => [k, process.env[k]]));
  let engine: EngineManager | undefined;
  let judge: Server | undefined;
  const receivers: Receiver[] = [];
  let scores: number[] = [];

  beforeEach(async () => {
    scores = [];
    judge = createServer((req, res) => {
      req.resume();
      req.on("end", () => {
        res.setHeader("content-type", "application/json");
        res.end(
          JSON.stringify({
            model: "fake-model",
            choices: [{ message: { content: JSON.stringify({ score: scores.shift() ?? 0.9, explanation: "fake" }) } }],
            usage: { prompt_tokens: 10, completion_tokens: 5 },
          }),
        );
      });
    });
    const base = await listen(judge);
    process.env["ATTEST_JUDGE_PROVIDER"] = "openai";
    process.env["ATTEST_JUDGE_BASE_URL"] = `${base}/v1`;
    process.env["ATTEST_JUDGE_API_KEY"] = "fake";
    process.env["ATTEST_JUDGE_MODEL"] = "fake-model";
    delete process.env["OPENAI_API_KEY"];
    process.env["ATTEST_CACHE_DIR"] = mkdtempSync(resolve(tmpdir(), "attest-drift-cache-"));
    engine = new EngineManager({ enginePath });
    await engine.start();
  });

  afterEach(async () => {
    await engine?.stop();
    engine = undefined;
    if (judge !== undefined) await closeServer(judge);
    judge = undefined;
    for (const receiver of receivers.splice(0)) await receiver.close();
    for (const key of ENV_KEYS) {
      const value = saved[key];
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
    vi.restoreAllMocks();
  });

  function judged(threshold: number) {
    return attestExpect(makeTrace(0)).passesJudge("Is it good?", { threshold }).assertions;
  }

  it("posts a drift alert to the webhook within 5 s of a drifting trace", async () => {
    const receiver = await startReceiver(200);
    receivers.push(receiver);
    // Ten good runs build the baseline; 0.7 passes the 0.6 static bar but is far below the baseline.
    scores = [...Array<number>(10).fill(0.9), 0.7];
    const runner = new ContinuousEvalRunner(new AttestClient(engine!), judged(0.6), {
      name: "drift-monitor",
      alertWebhook: receiver.url,
    });
    for (let i = 1; i <= 10; i++) expect(runner.submit(makeTrace(i))).toBe(true);
    const trace11 = makeTrace(11);
    expect(runner.submit(trace11)).toBe(true);

    const started = Date.now();
    runner.start();
    const arrived = await until(() => receiver.bodies.length > 0, 5_000);
    await runner.stop();

    expect(arrived).toBe(true);
    expect(Date.now() - started).toBeLessThan(5_000);
    expect(receiver.bodies).toHaveLength(1);
    const alert = receiver.bodies[0]!;
    expect(Object.keys(alert).sort()).toEqual(ALERT_KEYS);
    expect(alert).toMatchObject({
      runner: "drift-monitor",
      status: "drift_detected",
      result_status: "pass",
      score: 0.7,
      baseline_count: 10,
      static_threshold: 0.6,
      trace_id: trace11.trace_id,
    });
    expect(alert["assertion_id"]).toMatch(/^a2_[0-9a-f]{32}$/);
    expect(alert["baseline_mean"]).toBeCloseTo(0.9, 5);
  }, 30_000);

  it("posts a Slack-shaped alert and a hard-fail alert for a failing score", async () => {
    const slack = await startReceiver(200);
    const webhook = await startReceiver(200);
    receivers.push(slack, webhook);
    scores = [0.4];
    const runner = new ContinuousEvalRunner(new AttestClient(engine!), judged(0.8), {
      name: "slack-monitor",
      alertSlackUrl: slack.url,
      alertWebhook: webhook.url,
    });
    const trace = makeTrace(1);
    await runner.evaluateTrace(trace);

    expect(webhook.bodies).toHaveLength(1);
    const alert = webhook.bodies[0]!;
    expect(Object.keys(alert).sort()).toEqual(ALERT_KEYS);
    expect(alert).toMatchObject({
      runner: "slack-monitor",
      status: "hard_fail",
      result_status: "hard_fail",
      score: 0.4,
      static_threshold: 0.8,
      trace_id: trace.trace_id,
    });
    expect(alert["assertion_id"]).toMatch(/^a2_[0-9a-f]{32}$/);
    expect(typeof alert["baseline_count"] === "number" || alert["baseline_count"] === null).toBe(true);
    expect(slack.bodies).toHaveLength(1);
    expect(Object.keys(slack.bodies[0]!)).toEqual(["text"]);
    expect(slack.bodies[0]!["text"]).toBe(
      `[attest] drift alert \u2014 runner=slack-monitor assertion=${String(alert["assertion_id"])} status=hard_fail ` +
        `score=0.4 baseline_mean=${String(alert["baseline_mean"])} baseline_count=${String(alert["baseline_count"])} ` +
        `static_threshold=0.8 trace_id=${trace.trace_id}`,
    );
  }, 30_000);

  it("sends no alert while scores are stable", async () => {
    const receiver = await startReceiver(200);
    receivers.push(receiver);
    scores = Array<number>(12).fill(0.9);
    const runner = new ContinuousEvalRunner(new AttestClient(engine!), judged(0.6), {
      name: "stable-monitor",
      alertWebhook: receiver.url,
    });
    for (let i = 1; i <= 12; i++) await runner.evaluateTrace(makeTrace(i));
    await runner.stop();

    expect(receiver.bodies).toEqual([]);
  }, 30_000);

  it("emits a dispatch error when the receiver returns 500", async () => {
    const receiver = await startReceiver(500);
    receivers.push(receiver);
    scores = [0.3];
    const errors: unknown[] = [];
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const runner = new ContinuousEvalRunner(new AttestClient(engine!), judged(0.8), {
      name: "failing-receiver",
      alertWebhook: receiver.url,
      onError: (e) => errors.push(e),
    });
    const emitted: unknown[] = [];
    runner.alertEmitter.on("alertError", (e: unknown) => emitted.push(e));

    await expect(runner.evaluateTrace(makeTrace(1))).resolves.not.toBeNull();

    expect(errors).toHaveLength(1);
    expect(String(errors[0])).toMatch(/returned 500/);
    expect(emitted).toHaveLength(1);
    expect(console.error).toHaveBeenCalled();
  }, 30_000);

  it("emits a dispatch error when the receiver is down", async () => {
    const receiver = await startReceiver(200);
    const deadUrl = receiver.url;
    await receiver.close();
    scores = [0.3];
    const errors: unknown[] = [];
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const runner = new ContinuousEvalRunner(new AttestClient(engine!), judged(0.8), {
      name: "down-receiver",
      alertWebhook: deadUrl,
      onError: (e) => errors.push(e),
    });

    await expect(runner.evaluateTrace(makeTrace(1))).resolves.not.toBeNull();

    expect(errors).toHaveLength(1);
  }, 30_000);

  it("stop() waits for an in-flight evaluation and its dispatch", async () => {
    const receiver = await startReceiver(200, 400);
    receivers.push(receiver);
    scores = [0.3];
    const runner = new ContinuousEvalRunner(new AttestClient(engine!), judged(0.8), {
      name: "drain-monitor",
      alertWebhook: receiver.url,
    });
    const pending = runner.evaluateTrace(makeTrace(1));
    await until(() => false, 50);

    await runner.stop();

    // The receiver answers after 400 ms; stop() returned only after it did.
    expect(receiver.bodies).toHaveLength(1);
    await expect(pending).resolves.not.toBeNull();
  }, 30_000);
});
