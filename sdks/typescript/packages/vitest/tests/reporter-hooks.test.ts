import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { reportV2Validator } from "./report-schema.js";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const fixture = "tests/e2e/reporter-hooks.fixture.ts";

interface Report {
  tests_collected: number;
  tests: Array<{ test_id: string; name: string; file: string; line: number; outcome: string }>;
}

function runFixture(hook: string, scope: string, error = "Error"): Report {
  const dir = mkdtempSync(join(tmpdir(), "attest-reporter-hooks-"));
  const out = join(dir, "report.json");
  try {
    const proc = spawnSync(
      join(pkgDir, "node_modules/.bin/vitest"),
      ["run", "--config", "tests/e2e/vitest.reporter-hooks.config.ts", `--outputFile.attest=${out}`],
      {
        cwd: pkgDir,
        encoding: "utf8",
        timeout: 30_000,
        env: {
          PATH: process.env["PATH"] ?? "",
          HOME: tmpdir(),
          ATTEST_TEST_HOOK: hook,
          ATTEST_TEST_HOOK_SCOPE: scope,
          ATTEST_TEST_HOOK_ERROR: error,
        },
      },
    );
    expect(proc.status, proc.stdout + proc.stderr).toBe(hook === "none" ? 0 : 1);
    const report = JSON.parse(readFileSync(out, "utf8")) as Report;
    const validate = reportV2Validator();
    expect(validate(report), JSON.stringify(validate.errors)).toBe(true);
    expect(report.tests_collected).toBe(4);
    expect(report.tests).toHaveLength(4);
    expect(new Set(report.tests.map((test) => test.test_id)).size).toBe(4);
    for (const test of report.tests) {
      expect(test.file).toBe(fixture);
      expect(test.line).toBeGreaterThan(0);
    }
    return report;
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function outcomes(report: Report): Record<string, string> {
  return Object.fromEntries(report.tests.map((test) => [test.name, test.outcome]));
}

describe("AttestReporter suite hook failures", () => {
  it.each([
    ["beforeAll", "Error"],
    ["afterAll", "Error"],
    ["beforeAll", "AggregateError"],
    ["afterAll", "AggregateError"],
  ])(
    "reports a throwing suite %s (%s) as failed descendants, not passing siblings",
    (hook, error) => {
      expect(outcomes(runFixture(hook, "suite", error))).toEqual({
        "hooked test": "failed",
        "nested test": "failed",
        "untouched test": "passed",
        "intentional skip": "skipped",
      });
    },
    60_000,
  );

  it.each([
    ["beforeAll", "Error"],
    ["afterAll", "Error"],
    ["beforeAll", "AggregateError"],
    ["afterAll", "AggregateError"],
  ])(
    "reports a throwing module %s (%s) as failed descendants",
    (hook, error) => {
      expect(outcomes(runFixture(hook, "module", error))).toEqual({
        "hooked test": "failed",
        "nested test": "failed",
        "untouched test": "failed",
        "intentional skip": "failed",
      });
    },
    60_000,
  );

  it.each(["suite", "module"])(
    "reports an empty AggregateError in %s afterAll even beside a failed child",
    (scope) => {
      expect(outcomes(runFixture("test-and-afterAll", scope, "AggregateError"))).toEqual({
        "hooked test": "failed",
        "nested test": "failed",
        "untouched test": scope === "module" ? "failed" : "passed",
        "intentional skip": scope === "module" ? "failed" : "skipped",
      });
    },
    60_000,
  );

  it.each(["suite", "module"])(
    "reports failed descendants when %s cleanup rejects an empty AggregateError",
    (scope) => {
      const reported = outcomes(runFixture("cleanup", scope, "AggregateError"));
      expect(reported).toMatchObject({ "hooked test": "failed", "nested test": "failed" });
      if (scope === "module") {
        expect(reported).toMatchObject({ "untouched test": "failed", "intentional skip": "failed" });
      } else {
        // Vitest 3 preserves the cleanup error for precise suite attribution;
        // 4/5 flatten it, so the conservative module fallback also fails siblings.
        expect(["passed", "failed"]).toContain(reported["untouched test"]);
        expect(["skipped", "failed"]).toContain(reported["intentional skip"]);
      }
    },
    60_000,
  );

  it("leaves a passing suite and intentional skips unchanged", () => {
    expect(outcomes(runFixture("none", "suite"))).toEqual({
      "hooked test": "passed",
      "nested test": "passed",
      "untouched test": "passed",
      "intentional skip": "skipped",
    });
  }, 60_000);

  it("does not spread an ordinary test failure through the failed suite state", () => {
    expect(outcomes(runFixture("test", "suite"))).toEqual({
      "hooked test": "failed",
      "nested test": "passed",
      "untouched test": "passed",
      "intentional skip": "skipped",
    });
  }, 60_000);
});
