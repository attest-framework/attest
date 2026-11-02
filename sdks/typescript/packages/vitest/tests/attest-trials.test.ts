/**
 * trials() end to end: a vitest subprocess against the real engine writes a
 * report-v2 file with per-trial rows under stable assertion IDs, thrown trials as
 * hard_fail rows, the per-test budget enforced by the engine, and summary.usage.
 * Skipped when the engine binary has not been built (`make engine`).
 */
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { deterministicAssertionId } from "../src/test-state.js";
import { engine, reportV2Validator } from "./report-schema.js";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const fixture = "tests/e2e/trials.fixture.ts";

interface Row {
  assertion_id: string;
  status: string;
  reason?: string;
  explanation: string;
  failure_class?: string;
  trial?: number;
  test_id: string;
}

interface Report {
  summary: { usage: Record<string, number> } & Record<string, unknown>;
  results: Row[];
  tests: Array<{ test_id: string; name: string; outcome: string; trials?: number; assertion_ids: string[] }>;
}

describe.skipIf(!existsSync(engine))("trials() (real engine)", () => {
  it("reports every trial's rows, thrown trials, the test budget and usage", () => {
    const out = join(mkdtempSync(join(tmpdir(), "attest-trials-")), "r.json");
    const proc = spawnSync(
      join(pkgDir, "node_modules/.bin/vitest"),
      ["run", "--config", "tests/e2e/vitest.trials.config.ts", `--outputFile.attest=${out}`],
      {
        cwd: pkgDir,
        encoding: "utf8",
        env: {
          PATH: process.env["PATH"] ?? "",
          HOME: tmpdir(),
          ATTEST_ENGINE_PATH: engine,
          ATTEST_ENGINE_NO_DOWNLOAD: "1",
          // A configured judge on a closed local port: the test budget refuses
          // every call before it is sent, so nothing reaches the network.
          ATTEST_JUDGE_PROVIDER: "openai",
          ATTEST_JUDGE_MODEL: "gpt-4.1",
          ATTEST_JUDGE_BASE_URL: "http://127.0.0.1:9/v1",
          ATTEST_JUDGE_API_KEY: "unused",
        },
      },
    );
    const output = proc.stdout + proc.stderr;
    expect(proc.status, output).toBe(0);

    const report = JSON.parse(readFileSync(out, "utf8")) as Report;
    const validate = reportV2Validator();
    expect(validate(report), JSON.stringify(validate.errors)).toBe(true);

    const byName = new Map(report.tests.map((t) => [t.name, t]));
    const refunds = byName.get("refunds once")!;
    expect(refunds).toMatchObject({ outcome: "passed", trials: 4 });
    expect(refunds.test_id).toBe(`${fixture} > refund desk > refunds once`);
    expect([...refunds.assertion_ids].sort()).toEqual(
      [deterministicAssertionId(refunds.test_id, 0), deterministicAssertionId(refunds.test_id, 1)].sort(),
    );

    const rows = report.results.filter((r) => r.test_id === refunds.test_id);
    expect(rows.map((r) => r.trial).sort()).toEqual([1, 1, 2, 2, 3, 3, 4, 4]);
    expect(rows.filter((r) => r.trial === 2)).toEqual([
      expect.objectContaining({
        status: "hard_fail",
        explanation: "trial 2 raised Error: model server returned HTTP 500",
        failure_class: "broken_code",
      }),
      expect.objectContaining({ status: "hard_fail" }),
    ]);
    expect(rows.filter((r) => r.status === "hard_fail").map((r) => r.trial).sort()).toEqual([2, 2, 3]);

    const judge = byName.get("judge without calls left")!;
    expect(judge).toMatchObject({ outcome: "skipped", trials: 2 });
    expect(report.results.filter((r) => r.test_id === judge.test_id).map((r) => r.reason)).toEqual([
      "budget_exhausted",
      "budget_exhausted",
    ]);

    expect(report.summary.usage).toMatchObject({ model_calls: 0, judge_calls: 0, budget_refused: 2 });
    expect(output).toContain(`${refunds.test_id}: 2/4 trials passed (0 not evaluated)`);
    expect(output).toContain(
      "attest usage: 0 model calls · 0 tokens (0 cached) · $0.0000 · retry cost $0.0000 · 2 calls refused by budget",
    );
  }, 60_000);
});
