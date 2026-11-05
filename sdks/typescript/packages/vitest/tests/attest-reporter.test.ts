/**
 * AttestReporter end to end: a vitest subprocess against the real engine writes a
 * report-v2 file whose tests and rows carry file, line and outcome. Skipped when the
 * engine binary has not been built (`make engine`).
 */
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { engine, reportV2Validator } from "./report-schema.js";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const fixture = "tests/e2e/reporter.fixture.ts";

interface Report {
  tests_collected: number;
  summary: Record<string, number>;
  results: Array<{ status: string; test_id: string; file: string; line: number }>;
  tests: Array<{ name: string; file: string; line: number; outcome: string; not_evaluated_reasons?: string[] }>;
}

function lineOf(title: string): number {
  const lines = readFileSync(join(pkgDir, fixture), "utf8").split("\n");
  return lines.findIndex((l) => l.includes(`("${title}"`)) + 1;
}

describe.skipIf(!existsSync(engine))("AttestReporter (real engine)", () => {
  it("writes every test with outcome, file and line, and tags every row", () => {
    const out = join(mkdtempSync(join(tmpdir(), "attest-reporter-")), "nested", "r.json");
    const proc = spawnSync(
      join(pkgDir, "node_modules/.bin/vitest"),
      ["run", "--config", "tests/e2e/vitest.reporter.config.ts", `--outputFile.attest=${out}`],
      {
        cwd: pkgDir,
        encoding: "utf8",
        env: {
          PATH: process.env["PATH"] ?? "",
          HOME: tmpdir(),
          ATTEST_ENGINE_PATH: engine,
          ATTEST_ENGINE_NO_DOWNLOAD: "1",
        },
      },
    );
    expect(proc.status, proc.stdout + proc.stderr).toBe(1);

    const report = JSON.parse(readFileSync(out, "utf8")) as Report;
    const validate = reportV2Validator();
    expect(validate(report), JSON.stringify(validate.errors)).toBe(true);
    const byName = new Map(report.tests.map((t) => [t.name, t]));
    expect([...byName.keys()].sort()).toEqual(
      ["fails", "flaky", "no attest", "not evaluated", "passes", "plain skip"].sort(),
    );
    for (const [name, outcome] of [
      ["passes", "passed"],
      ["fails", "failed"],
      ["flaky", "passed"],
      ["not evaluated", "skipped"],
      ["plain skip", "skipped"],
      ["no attest", "passed"],
    ] as const) {
      expect(byName.get(name)).toMatchObject({ file: fixture, line: lineOf(name), outcome });
    }
    expect(byName.get("not evaluated")?.not_evaluated_reasons).toEqual(["provider_not_configured"]);
    const failing = report.results.filter((r) => r.status === "hard_fail");
    expect(failing).toEqual([
      expect.objectContaining({ test_id: `${fixture} > fails`, file: fixture, line: lineOf("fails") }),
    ]);
    expect(report.tests_collected).toBe(6);
    // The retried test reports only its passing attempt.
    expect(report.summary).toMatchObject({ total: 5, passed: 3, hard_fail: 1, not_evaluated: 1 });
  }, 60_000);

  it.each(["threads", "forks"])(
    "aggregates every test and failing row across files under --pool=%s",
    (pool) => {
      const out = join(mkdtempSync(join(tmpdir(), "attest-reporter-pool-")), "r.json");
      const proc = spawnSync(
        join(pkgDir, "node_modules/.bin/vitest"),
        ["run", "--config", "tests/e2e/vitest.pool.config.ts", `--pool=${pool}`, `--outputFile.attest=${out}`],
        {
          cwd: pkgDir,
          encoding: "utf8",
          env: {
            PATH: process.env["PATH"] ?? "",
            HOME: tmpdir(),
            ATTEST_ENGINE_PATH: engine,
            ATTEST_ENGINE_NO_DOWNLOAD: "1",
          },
        },
      );
      expect(proc.status, proc.stdout + proc.stderr).toBe(1);

      const report = JSON.parse(readFileSync(out, "utf8")) as Report;
      const validate = reportV2Validator();
      expect(validate(report), JSON.stringify(validate.errors)).toBe(true);
      // Three files: 3 passing, 6 failing, 1 skipped.
      expect(report.tests_collected).toBe(10);
      expect(report.tests).toHaveLength(10);
      expect(new Set(report.tests.map((t) => t.file))).toEqual(
        new Set(["a", "b", "c"].map((n) => `tests/e2e/pool/${n}.fixture.ts`)),
      );
      expect(report.tests.filter((t) => t.outcome === "failed")).toHaveLength(6);
      expect(report.results.filter((r) => r.status === "hard_fail")).toHaveLength(6);
      expect(report.summary).toMatchObject({ total: 9, passed: 3, hard_fail: 6, not_evaluated: 0 });
    },
    60_000,
  );
});
