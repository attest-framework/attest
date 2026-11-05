/**
 * Responses are routed by request id: a test's verdict comes from its own request.
 *
 * Each case runs tests/e2e/*.transport.ts in a vitest subprocess against the real engine
 * (`make engine`). Skipped when the engine binary has not been built.
 */
import { spawn } from "node:child_process";
import { chmodSync, existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { ENGINE_VERSION } from "@attest-ai/core";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const engine = resolve(pkgDir, "../../../../bin/attest-engine");

interface JsonAssertion {
  title: string;
  status: string;
  failureMessages: string[];
}

interface Run {
  status: number | null;
  elapsedMs: number;
  stdout: string;
  byTitle: Map<string, JsonAssertion>;
}

function runFixture(file: string, extraEnv: Record<string, string> = {}): Promise<Run> {
  const dir = mkdtempSync(join(tmpdir(), "attest-transport-"));
  const out = join(dir, "report.json");
  const env: Record<string, string> = {
    PATH: process.env["PATH"] ?? "",
    HOME: dir,
    COLUMNS: "200",
    ATTEST_ENGINE_PATH: engine,
    ATTEST_ENGINE_NO_DOWNLOAD: "1",
    ...extraEnv,
  };
  const started = Date.now();
  return new Promise((resolveRun, reject) => {
    const proc = spawn(
      join(pkgDir, "node_modules/.bin/vitest"),
      [
        "run",
        "--config",
        "tests/e2e/vitest.transport.config.ts",
        file,
        "--reporter=verbose",
        "--reporter=json",
        `--outputFile.json=${out}`,
      ],
      { cwd: pkgDir, env },
    );
    let stdout = "";
    proc.stdout.on("data", (chunk: Buffer) => (stdout += chunk.toString()));
    proc.stderr.on("data", (chunk: Buffer) => (stdout += chunk.toString()));
    proc.on("error", reject);
    proc.on("close", (status) => {
      const byTitle = new Map<string, JsonAssertion>();
      if (existsSync(out)) {
        const report = JSON.parse(readFileSync(out, "utf8")) as {
          testResults: Array<{ assertionResults: JsonAssertion[] }>;
        };
        for (const f of report.testResults) {
          for (const a of f.assertionResults) byTitle.set(a.title, a);
        }
      }
      resolveRun({ status, elapsedMs: Date.now() - started, stdout, byTitle });
    });
  });
}

function statuses(run: Run): Record<string, string | undefined> {
  return Object.fromEntries([...run.byTitle].map(([title, a]) => [title, a.status]));
}

describe.skipIf(!existsSync(engine))("id-routed transport (real engine)", () => {
  it("a drift_alert line before A's response does not shift B and C", async () => {
    const run = await runFixture("tests/e2e/drift.transport.ts");

    expect(statuses(run)).toEqual({
      "A dynamic content hard-fail": "failed",
      "B passes": "passed",
      "C fails": "failed",
    });
  }, 60_000);

  it("a forced ATTEST_ENGINE_TIMEOUT in one test leaves the next failing test failing", async () => {
    const run = await runFixture("tests/e2e/timeout.transport.ts");

    expect(statuses(run)).toEqual({
      "T1 times out": "failed",
      "T2 fails on its own verdict": "failed",
    });
    expect(run.stdout).toContain("expected:   contains \"absent-in-t2\"");
  }, 60_000);

  it("two it.concurrent tests, one failing, report one pass and one fail in 20/20 runs", async () => {
    const runs: Run[] = [];
    for (let batch = 0; batch < 4; batch++) {
      runs.push(
        ...(await Promise.all(
          Array.from({ length: 5 }, () => runFixture("tests/e2e/concurrent.transport.ts")),
        )),
      );
    }

    expect(runs).toHaveLength(20);
    for (const run of runs) {
      expect(statuses(run)).toEqual({
        "concurrent passes": "passed",
        "concurrent fails": "failed",
      });
    }
  }, 180_000);

  it("an engine that exits during startup fails the fixture within 10 s showing its stderr", async () => {
    const dir = mkdtempSync(join(tmpdir(), "attest-dead-engine-"));
    const dead = join(dir, "attest-engine");
    writeFileSync(
      dead,
      [
        "#!/bin/sh",
        'if [ "$1" = version ]; then echo "attest-engine ' + ENGINE_VERSION + '"; exit 0; fi',
        'echo "engine boot failure: bad configuration" >&2',
        "exit 3",
        "",
      ].join("\n"),
    );
    chmodSync(dead, 0o755);

    const run = await runFixture("tests/e2e/startup-exit.transport.ts", { ATTEST_ENGINE_PATH: dead });

    expect(run.elapsedMs).toBeLessThan(10_000);
    expect(run.byTitle.get("startup exit")?.status).toBe("failed");
    const message = run.byTitle.get("startup exit")?.failureMessages.join("\n") ?? "";
    expect(message).toContain("engine boot failure: bad configuration");
  }, 30_000);
});
