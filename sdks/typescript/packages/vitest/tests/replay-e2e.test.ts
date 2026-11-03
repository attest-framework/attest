/**
 * useReplay end to end against the real engine: capture a scripted agent, then replay it
 * in a vitest subprocess with no provider keys and the egress guard on. Skipped when the
 * engine binary has not been built (`make engine`).
 */
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import * as http from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { engine, reportV2Validator } from "./report-schema.js";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");

interface Report {
  summary: { not_evaluated: number; replay?: Record<string, number> };
  results: Array<{ status: string; reason?: string; assertion_id: string }>;
  tests: Array<{
    name: string;
    outcome: string;
    not_evaluated_reasons?: string[];
    replay?: { fixtures: string[]; mode: string; replayed: number; live: number; misses: number; miss?: string };
  }>;
}

describe.skipIf(!existsSync(engine))("useReplay (real engine)", () => {
  let upstream: http.Server;
  let dir: string;

  beforeAll(async () => {
    upstream = http.createServer((req, res) => {
      req.resume();
      req.on("end", () => {
        res.setHeader("content-type", "application/json");
        res.end(
          JSON.stringify({
            id: `chatcmpl-${Math.random().toString(16).slice(2)}${Date.now()}`,
            object: "chat.completion",
            created: Math.floor(Date.now() / 1000),
            model: "gpt-test",
            choices: [{ index: 0, finish_reason: "stop", message: { role: "assistant", content: "done" } }],
          }),
        );
      });
    });
    await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
    const port = (upstream.address() as { port: number }).port;
    dir = mkdtempSync(join(tmpdir(), "attest-replay-e2e-"));
    const capture = await new Promise<{ code: number | null; stderr: string }>((resolveRun) => {
      const child = spawn(
        engine,
        ["capture", "--name", "inc-1042", "--out-dir", join(dir, "fixtures"), "--", process.execPath, join(pkgDir, "tests/e2e/replay-agent.mjs")],
        {
          cwd: dir,
          env: { PATH: process.env["PATH"] ?? "", HOME: dir, OPENAI_BASE_URL: `http://127.0.0.1:${String(port)}/v1` },
        },
      );
      let stderr = "";
      child.stderr.on("data", (c: Buffer) => (stderr += c.toString()));
      child.on("close", (code) => resolveRun({ code, stderr }));
    });
    expect(capture.code, capture.stderr).toBe(0);
  }, 60_000);

  afterAll(() => {
    upstream.close();
  });

  function run(env: Record<string, string>, testFile?: string) {
    const out = join(dir, `report-${Math.random().toString(16).slice(2)}.json`);
    const proc = spawnSync(
      join(pkgDir, "node_modules/.bin/vitest"),
      ["run", "--config", "tests/e2e/vitest.replay.config.ts", `--outputFile.attest=${out}`],
      {
        cwd: pkgDir,
        encoding: "utf8",
        env: {
          PATH: process.env["PATH"] ?? "",
          HOME: dir,
          ATTEST_ENGINE_PATH: engine,
          ATTEST_ENGINE_NO_DOWNLOAD: "1",
          ATTEST_EGRESS: "block",
          REPLAY_FIXTURES: join(dir, "fixtures"),
          // Tests report to the engine's upstream only through the replay proxy; this one is dead.
          OPENAI_BASE_URL: "http://127.0.0.1:1/v1",
          ...(testFile === undefined ? {} : { REPLAY_TEST_FILE: testFile }),
          ...env,
        },
      },
    );
    const report = existsSync(out) ? (JSON.parse(readFileSync(out, "utf8")) as Report) : undefined;
    return { proc, output: proc.stdout + proc.stderr, report };
  }

  it("strict replay of the unchanged agent makes zero live calls and the report says so", () => {
    const { proc, output, report } = run({ ATTEST_REPLAY: "strict" });

    expect(proc.status, output).toBe(0);
    expect(output).toContain("0 live model calls · $0.00");
    const validate = reportV2Validator();
    expect(validate(report), JSON.stringify(validate.errors)).toBe(true);
    expect(report?.summary.replay).toEqual({ replayed: 1, live: 0, misses: 0, live_cost_usd: 0, unpriced_live_calls: 0 });
    expect(report?.tests.find((t) => t.name === "replays inc-1042")?.replay).toEqual({
      fixtures: ["inc-1042"],
      mode: "strict",
      replayed: 1,
      live: 0,
      misses: 0,
    });
  }, 60_000);

  it("strict replay fails with the exact miss message when the system prompt changed", () => {
    const { proc, output, report } = run({ ATTEST_REPLAY: "strict", REPLAY_SYSTEM: "You refund twice." });

    expect(proc.status, output).toBe(1);
    expect(output).toMatch(
      /replay miss: system prompt changed \(hash [0-9a-f]{7}→[0-9a-f]{7}\); re-record with `attest capture --refresh inc-1042`/,
    );
    expect(output).not.toContain("model call failed");
    expect(report?.tests.find((t) => t.name === "replays inc-1042")).toMatchObject({
      outcome: "failed",
      replay: { mode: "strict", replayed: 0, misses: 1 },
    });
  }, 60_000);

  it("strict miss fails an it.fails test with the miss message (the marker does not hide it)", () => {
    const { proc, output, report } = run(
      { ATTEST_REPLAY: "strict", REPLAY_SYSTEM: "You refund twice." },
      "tests/e2e/replay-fails.fixture.ts",
    );

    expect(proc.status, output).toBe(1);
    expect(output).toMatch(
      /replay miss: system prompt changed \(hash [0-9a-f]{7}→[0-9a-f]{7}\); re-record with `attest capture --refresh inc-1042`/,
    );
    expect(report?.tests.find((t) => t.name === "incident reproduces under it.fails")?.outcome).toBe("failed");
  }, 60_000);

  it("an it.fails replay test without a miss still passes as an expected failure", () => {
    const { proc, output, report } = run({ ATTEST_REPLAY: "strict" }, "tests/e2e/replay-fails.fixture.ts");

    expect(proc.status, output).toBe(0);
    expect(report?.tests.find((t) => t.name === "incident reproduces under it.fails")).toMatchObject({
      outcome: "passed",
      replay: { replayed: 1, misses: 0 },
    });
  }, 60_000);

  it("auto replay without a key skips as not evaluated: replay_miss", () => {
    const { proc, output, report } = run({ ATTEST_REPLAY: "auto", REPLAY_SYSTEM: "You refund twice." });

    expect(proc.status, output).toBe(0);
    const validate = reportV2Validator();
    expect(validate(report), JSON.stringify(validate.errors)).toBe(true);
    expect(report?.tests.find((t) => t.name === "replays inc-1042")).toMatchObject({
      outcome: "skipped",
      not_evaluated_reasons: ["replay_miss"],
    });
    expect(report?.results).toEqual([
      expect.objectContaining({ status: "not_evaluated", reason: "replay_miss", assertion_id: "replay_inc-1042" }),
    ]);
    expect(report?.summary.not_evaluated).toBe(1);
  }, 60_000);

  it("off leaves the environment alone", () => {
    const { proc, output, report } = run({ ATTEST_REPLAY: "off" });

    // With replay off the agent reaches its own (dead) upstream, not the proxy.
    expect(proc.status, output).toBe(1);
    expect(output).toContain("fetch failed");
    expect(report?.summary.replay).toBeUndefined();
  }, 60_000);

  it("useWorld composes with useReplay in one test and the world ends with the test", () => {
    const { proc, output } = run(
      { ATTEST_REPLAY: "strict", WORLD_FILE: join(pkgDir, "../../tests/integration/data/payments.world.yaml") },
      "tests/e2e/world.fixture.ts",
    );

    expect(proc.status, output).toBe(0);
    expect(output).toContain("2 passed");
    expect(output).toContain("0 live model calls · $0.00");
  }, 60_000);
});
