/**
 * The published subpaths work in the Vitest main process, on whichever Vitest is installed:
 * `reporters: ["@attest-ai/vitest/reporter"]` and `globalSetup: ["@attest-ai/vitest/setup"]`
 * load, the report carries the run's cost, and the run prints the cost summary line. The judge
 * is a local fake. Needs `pnpm build` (the subpaths resolve to dist/) and `make engine`.
 */
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { beforeAll, describe, expect, it } from "vitest";
import { engine, reportV2Validator } from "./report-schema.js";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");

interface Report {
  total_cost: number;
  tests: Array<{ name: string; outcome: string }>;
  summary: { usage?: { model_calls: number; cost_usd: number } };
}

let report: Report;
let output = "";
let exitCode: number | null;

async function runFixture(): Promise<void> {
  const judge = createServer((req, res) => {
    req.resume();
    req.on("end", () => {
      res.setHeader("content-type", "application/json");
      res.end(
        JSON.stringify({
          model: "gpt-4.1",
          choices: [{ message: { content: '{"score": 0.95, "explanation": "polite"}' } }],
          usage: { prompt_tokens: 1000, completion_tokens: 100 },
        }),
      );
    });
  });
  await new Promise<void>((ok) => judge.listen(0, "127.0.0.1", ok));
  const port = (judge.address() as AddressInfo).port;
  const reportPath = join(mkdtempSync(join(tmpdir(), "attest-packaged-")), "r.json");
  const proc = spawn(
    join(pkgDir, "node_modules/.bin/vitest"),
    ["run", "--config", "tests/e2e/vitest.packaged.config.ts", `--outputFile.attest=${reportPath}`],
    {
      cwd: pkgDir,
      env: {
        PATH: process.env["PATH"] ?? "",
        HOME: tmpdir(),
        ATTEST_ENGINE_PATH: engine,
        ATTEST_ENGINE_NO_DOWNLOAD: "1",
        ATTEST_JUDGE_PROVIDER: "openai",
        ATTEST_JUDGE_MODEL: "gpt-4.1",
        ATTEST_JUDGE_BASE_URL: `http://127.0.0.1:${port}/v1`,
        ATTEST_JUDGE_API_KEY: "unused",
      },
    },
  );
  proc.stdout.on("data", (d: Buffer) => (output += d.toString()));
  proc.stderr.on("data", (d: Buffer) => (output += d.toString()));
  exitCode = await new Promise<number | null>((done) => proc.on("close", done));
  judge.close();
  if (!existsSync(reportPath)) throw new Error(`no report written (exit ${exitCode}):\n${output}`);
  report = JSON.parse(readFileSync(reportPath, "utf8")) as Report;
}

describe.skipIf(!existsSync(engine))("published subpaths (real engine, fake judge)", () => {
  beforeAll(runFixture, 120_000);

  it("runs the test green through the setup and reporter subpaths", () => {
    expect(exitCode, output).toBe(0);
    expect(report.tests).toMatchObject([{ name: "judged", outcome: "passed" }]);
    const validate = reportV2Validator();
    expect(validate(report), JSON.stringify(validate.errors)).toBe(true);
  });

  it("reports the judge cost in the report and prints the usage summary", () => {
    expect(report.summary.usage?.model_calls).toBe(1);
    expect(report.summary.usage?.cost_usd).toBeGreaterThan(0);
    expect(report.total_cost).toBeGreaterThan(0);
    expect(output).toMatch(/attest usage: 1 model calls · 1100 tokens/);
  });
});
