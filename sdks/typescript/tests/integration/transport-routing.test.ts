import { spawn } from "node:child_process";
import { existsSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it } from "vitest";
import {
  AttestClient,
  EngineManager,
  EngineTimeoutError,
  ProtocolDesyncError,
  TraceBuilder,
  attestExpect,
} from "../../packages/core/src/index.js";

const here = dirname(fileURLToPath(import.meta.url));
const enginePath = resolve(here, "../../../../bin/attest-engine");

const JUDGE_ENV_KEYS = [
  "ATTEST_JUDGE_PROVIDER",
  "ATTEST_JUDGE_BASE_URL",
  "ATTEST_JUDGE_API_KEY",
  "ATTEST_JUDGE_MODEL",
  "ATTEST_MAX_JUDGE_CALLS",
  "ATTEST_BUDGET_USD",
  "ATTEST_ENGINE_TIMEOUT",
  "OPENAI_API_KEY",
  "ATTEST_CACHE_DIR",
] as const;

describe.skipIf(!existsSync(enginePath))("id-routed transport (real engine)", () => {
  const saved = Object.fromEntries(JUDGE_ENV_KEYS.map((k) => [k, process.env[k]]));
  let engine: EngineManager | undefined;
  let judge: Server | undefined;

  afterEach(async () => {
    await engine?.stop();
    engine = undefined;
    await new Promise<void>((done) => (judge === undefined ? done() : judge.close(() => done())));
    judge = undefined;
    for (const key of JUDGE_ENV_KEYS) {
      const value = saved[key];
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });

  it("a standalone script exits within 1 s of finishing its work", async () => {
    const script = resolve(here, "../support/standalone-script.mjs");
    const child = spawn(process.execPath, [script], {
      env: { ...process.env, ATTEST_ENGINE_PATH: enginePath, ATTEST_ENGINE_NO_DOWNLOAD: "1" },
    });
    let doneAt = 0;
    let output = "";
    child.stdout.on("data", (chunk: Buffer) => {
      output += chunk.toString();
      if (doneAt === 0 && output.includes("WORK_DONE")) doneAt = Date.now();
    });
    child.stderr.on("data", (chunk: Buffer) => (output += chunk.toString()));
    const guard = setTimeout(() => child.kill("SIGKILL"), 20_000);

    const code = await new Promise<number | null>((done) => child.on("close", done));
    clearTimeout(guard);

    expect(output).toContain("WORK_DONE 1");
    expect(code).toBe(0);
    expect(Date.now() - doneAt).toBeLessThan(1_000);
  }, 30_000);

  it("rejects with ProtocolDesyncError when the returned assertion ids differ from the sent set", async () => {
    engine = new EngineManager({ enginePath });
    await engine.start();
    const client = new AttestClient(engine);
    const trace = new TraceBuilder("agent").setOutput({ message: "ok" }).build();
    const assertions = attestExpect(trace).outputContains("ok").assertions;
    const real = engine.client.sendRequest.bind(engine.client);
    engine.client.sendRequest = async (method, params, timeoutMs) => {
      const raw = (await real(method, params, timeoutMs)) as { results: Array<{ assertion_id: string }> };
      return { ...raw, results: raw.results.map((r) => ({ ...r, assertion_id: "someone-elses" })) };
    };

    await expect(client.evaluateBatch(trace, assertions)).rejects.toBeInstanceOf(ProtocolDesyncError);
  }, 30_000);

  it("ATTEST_MAX_JUDGE_CALLS=2: a timeout plus restart never makes more than 2 judge calls", async () => {
    const calls: number[] = [];
    judge = createServer((req, res) => {
      req.resume();
      req.on("end", () => {
        calls.push(Date.now());
        const reply = (): void => {
          res.setHeader("content-type", "application/json");
          res.end(
            JSON.stringify({
              model: "fake-model",
              choices: [{ message: { content: '{"score": 0.9, "explanation": "fine"}' } }],
              usage: { prompt_tokens: 10, completion_tokens: 5 },
            }),
          );
        };
        // The first call outlives the request timeout; later calls answer at once.
        if (calls.length === 1) setTimeout(reply, 2_000);
        else reply();
      });
    });
    await new Promise<void>((done) => judge!.listen(0, "127.0.0.1", done));
    const port = (judge.address() as AddressInfo).port;
    process.env["ATTEST_JUDGE_PROVIDER"] = "openai";
    process.env["ATTEST_JUDGE_BASE_URL"] = `http://127.0.0.1:${port}/v1`;
    process.env["ATTEST_JUDGE_API_KEY"] = "fake";
    process.env["ATTEST_JUDGE_MODEL"] = "fake-model";
    process.env["ATTEST_MAX_JUDGE_CALLS"] = "2";
    // The engine caches judge verdicts; a fresh cache keeps every batch a real judge call.
    process.env["ATTEST_CACHE_DIR"] = mkdtempSync(resolve(tmpdir(), "attest-judge-cache-"));

    engine = new EngineManager({ enginePath });
    await engine.start();
    const client = new AttestClient(engine);
    const trace = new TraceBuilder("agent").setOutput({ message: "ok" }).build();
    const judged = attestExpect(trace).passesJudge("Is it ok?").assertions;

    await expect(
      client.evaluateBatch(trace, judged, { timeout: 400, budget: { max_judge_calls: 1 } }),
    ).rejects.toBeInstanceOf(EngineTimeoutError);
    const second = await client.evaluateBatch(trace, judged);
    const third = await client.evaluateBatch(trace, attestExpect(trace).passesJudge("Is it fine?").assertions);

    expect(second.results[0]?.status).toBe("pass");
    expect(third.results[0]?.status).toBe("not_evaluated");
    expect(third.results[0]?.reason).toBe("budget_exhausted");
    expect(calls.length).toBeLessThanOrEqual(2);
  }, 60_000);

  it("ATTEST_MAX_JUDGE_CALLS=5: simulated-user turns make spend unknown, so a timeout restart gets no budget", async () => {
    const calls: number[] = [];
    judge = createServer((req, res) => {
      req.resume();
      req.on("end", () => {
        calls.push(Date.now());
        const reply = (): void => {
          res.setHeader("content-type", "application/json");
          res.end(
            JSON.stringify({
              model: "fake-model",
              choices: [{ message: { content: '{"score": 0.9, "explanation": "fine"}' } }],
              usage: { prompt_tokens: 10, completion_tokens: 5 },
            }),
          );
        };
        // The fourth call (the judge batch) outlives the request timeout.
        if (calls.length === 4) setTimeout(reply, 2_000);
        else reply();
      });
    });
    await new Promise<void>((done) => judge!.listen(0, "127.0.0.1", done));
    const port = (judge.address() as AddressInfo).port;
    process.env["ATTEST_JUDGE_PROVIDER"] = "openai";
    process.env["ATTEST_JUDGE_BASE_URL"] = `http://127.0.0.1:${port}/v1`;
    process.env["ATTEST_JUDGE_API_KEY"] = "fake";
    process.env["ATTEST_JUDGE_MODEL"] = "fake-model";
    process.env["ATTEST_MAX_JUDGE_CALLS"] = "5";
    process.env["ATTEST_CACHE_DIR"] = mkdtempSync(resolve(tmpdir(), "attest-judge-cache-"));

    engine = new EngineManager({ enginePath });
    await engine.start();
    const client = new AttestClient(engine);
    const persona = { name: "p", system_prompt: "be a user", style: "terse", temperature: 0.2 };
    for (let turn = 0; turn < 3; turn++) {
      await client.generateUserMessage(persona, [{ role: "user", content: `turn ${turn}` }]);
    }
    const trace = new TraceBuilder("agent").setOutput({ message: "ok" }).build();

    await expect(
      client.evaluateBatch(trace, attestExpect(trace).passesJudge("Is it ok?").assertions, {
        timeout: 400,
        budget: { max_judge_calls: 1 },
      }),
    ).rejects.toBeInstanceOf(EngineTimeoutError);
    for (const question of ["Is it fine?", "Is it good?", "Is it right?"]) {
      await client.evaluateBatch(trace, attestExpect(trace).passesJudge(question).assertions);
    }

    expect(calls.length).toBeLessThanOrEqual(5);
  }, 60_000);

  it("ATTEST_MAX_JUDGE_CALLS=5: a failed simulated-user turn makes spend unknown, so a timeout restart gets no budget", async () => {
    const calls: number[] = [];
    judge = createServer((req, res) => {
      req.resume();
      req.on("end", () => {
        calls.push(Date.now());
        const n = calls.length;
        // The simulated-user turn fails after reaching the provider (the engine retries 5xx);
        // the first judge batch call then outlives the request timeout.
        if (n <= 4) {
          res.statusCode = 500;
          res.end("upstream down");
          return;
        }
        const reply = (): void => {
          res.setHeader("content-type", "application/json");
          res.end(
            JSON.stringify({
              model: "fake-model",
              choices: [{ message: { content: '{"score": 0.9, "explanation": "fine"}' } }],
              usage: { prompt_tokens: 10, completion_tokens: 5 },
            }),
          );
        };
        if (n === 5) setTimeout(reply, 2_000);
        else reply();
      });
    });
    await new Promise<void>((done) => judge!.listen(0, "127.0.0.1", done));
    const port = (judge.address() as AddressInfo).port;
    process.env["ATTEST_JUDGE_PROVIDER"] = "openai";
    process.env["ATTEST_JUDGE_BASE_URL"] = `http://127.0.0.1:${port}/v1`;
    process.env["ATTEST_JUDGE_API_KEY"] = "fake";
    process.env["ATTEST_JUDGE_MODEL"] = "fake-model";
    process.env["ATTEST_MAX_JUDGE_CALLS"] = "5";
    process.env["ATTEST_CACHE_DIR"] = mkdtempSync(resolve(tmpdir(), "attest-judge-cache-"));

    engine = new EngineManager({ enginePath });
    await engine.start();
    const client = new AttestClient(engine);
    const persona = { name: "p", system_prompt: "be a user", style: "terse", temperature: 0.2 };
    await expect(client.generateUserMessage(persona, [{ role: "user", content: "hi" }])).rejects.toThrow();
    const failedTurnCalls = calls.length;
    const trace = new TraceBuilder("agent").setOutput({ message: "ok" }).build();

    await expect(
      client.evaluateBatch(trace, attestExpect(trace).passesJudge("Is it ok?").assertions, {
        timeout: 400,
        budget: { max_judge_calls: 1 },
      }),
    ).rejects.toBeInstanceOf(EngineTimeoutError);
    for (const question of ["Is it fine?", "Is it good?", "Is it right?"]) {
      await client.evaluateBatch(trace, attestExpect(trace).passesJudge(question).assertions);
    }

    expect(failedTurnCalls).toBeGreaterThan(0);
    expect(calls.length).toBeLessThanOrEqual(5);
  }, 60_000);
});
