/**
 * "Green means checked", Vitest side, against the real engine and a local fake judge:
 * unevaluated and empty chains fail their test, assertion ids follow the scheme-2 recipe and
 * stay distinct for identical assertions, a soft-failure budget breach is carried into the
 * report beside the hard fail, and a `.plugin()` assertion follows its plugin result while the
 * judge in the same batch still evaluates. Skipped when the engine binary has not been built.
 */
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { beforeAll, describe, expect, it } from "vitest";
import * as vitest from "vitest";
import { assertionIdOf } from "@attest-ai/core";
import { engine, reportV2Validator } from "./report-schema.js";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const fixture = "tests/e2e/checked.fixture.ts";

interface Row {
  assertion_id: string;
  status: string;
  type: string;
  test_id: string;
  scope_id?: string;
  spec_hash?: string;
  explanation?: string;
}
interface Report {
  id_scheme: number;
  budget_exceeded: boolean;
  results: Row[];
  tests: Array<{ name: string; outcome: string }>;
}
interface JsonRun {
  testResults: Array<{ assertionResults: Array<{ title: string; status: string; failureMessages: string[] }> }>;
}

let report: Report;
let failures: Map<string, string>;
let judgeRequests = 0;

async function runFixture(): Promise<void> {
  const judge = createServer((req, res) => {
    req.resume();
    req.on("end", () => {
      judgeRequests += 1;
      res.setHeader("content-type", "application/json");
      res.end(
        JSON.stringify({
          model: "gpt-4.1",
          choices: [{ message: { content: '{"score": 0.95, "explanation": "polite"}' } }],
          usage: { prompt_tokens: 10, completion_tokens: 5 },
        }),
      );
    });
  });
  await new Promise<void>((ok) => judge.listen(0, "127.0.0.1", ok));
  const port = (judge.address() as AddressInfo).port;
  const dir = mkdtempSync(join(tmpdir(), "attest-checked-"));
  const reportPath = join(dir, "r.json");
  const jsonPath = join(dir, "run.json");
  const proc = spawn(
    join(pkgDir, "node_modules/.bin/vitest"),
    [
      "run",
      "--config",
      "tests/e2e/vitest.checked.config.ts",
      `--outputFile.attest=${reportPath}`,
      `--outputFile.json=${jsonPath}`,
    ],
    {
      cwd: pkgDir,
      env: {
        PATH: process.env["PATH"] ?? "",
        HOME: tmpdir(),
        ATTEST_ENGINE_PATH: engine,
        ATTEST_ENGINE_NO_DOWNLOAD: "1",
        ATTEST_MAX_SOFT_FAILURES: "1",
        ATTEST_JUDGE_PROVIDER: "openai",
        ATTEST_JUDGE_MODEL: "gpt-4.1",
        ATTEST_JUDGE_BASE_URL: `http://127.0.0.1:${port}/v1`,
        ATTEST_JUDGE_API_KEY: "unused",
      },
    },
  );
  let output = "";
  proc.stdout.on("data", (d: Buffer) => (output += d.toString()));
  proc.stderr.on("data", (d: Buffer) => (output += d.toString()));
  await new Promise<void>((done) => proc.on("close", () => done()));
  judge.close();
  report = JSON.parse(readFileSync(reportPath, "utf8")) as Report;
  const run = JSON.parse(readFileSync(jsonPath, "utf8")) as JsonRun;
  failures = new Map(
    run.testResults.flatMap((f) => f.assertionResults).map((t) => [t.title, t.failureMessages.join("\n")]),
  );
  if (failures.size === 0) throw new Error(`fixture run produced no tests:\n${output}`);
}

function rowsOf(name: string): Row[] {
  return report.results.filter((r) => r.test_id === `${fixture} > ${name}`);
}

describe.skipIf(!existsSync(engine))("green means checked (real engine)", () => {
  beforeAll(runFixture, 120_000);

  it("writes a schema-valid report with id_scheme 2", () => {
    const validate = reportV2Validator();
    expect(validate(report), JSON.stringify(validate.errors)).toBe(true);
    expect(report.id_scheme).toBe(2);
  });

  it("fails a test that built a chain it never evaluated", () => {
    expect(failures.get("never evaluated")).toContain("1 expect() chain built but never evaluated");
    expect(failures.get("never evaluated")).not.toContain("1 expect() chains");
  });

  it("tracks a chain built by a hook registered before the integration", () => {
    expect(failures.get("chain in early beforeEach")).toContain(
      "1 expect() chain built but never evaluated",
    );
  });

  it("never silently accepts an unevaluated concurrent beforeEach chain", () => {
    expect(failures.get("never evaluates hook chain")).toMatch(
      /1 expect\(\) chain built but never evaluated|Attest cannot bind this concurrent test's chain guard/,
    );
    expect(report.tests.find((test) => test.name.endsWith("never evaluates hook chain"))?.outcome)
      .toBe("failed");
  });

  it("charges a shared chain only to the test that extends it", () => {
    expect(failures.get("evaluates shared chain")).toBe("");
    expect(failures.get("extends shared chain without evaluation")).toContain(
      "1 expect() chain built but never evaluated",
    );
    expect(failures.get("leaves shared chain untouched")).toBe("");
  });

  it("preserves assigned identities and allocates only new occurrence indices", () => {
    const testId = `${fixture} > re-evaluated duplicate identities`;
    const spec = { target: "output.message", check: "contains", value: "ok", case_sensitive: false, soft: false };
    expect(rowsOf("re-evaluated duplicate identities").map((row) => row.assertion_id)).toEqual([
      assertionIdOf(testId, "content", spec, 0),
      assertionIdOf(testId, "content", spec, 0),
      assertionIdOf(testId, "content", spec, 1),
    ]);
    expect(failures.get("re-evaluated duplicate identities")).toBe("");
  });

  it("fails a chain with an assertion appended after evaluation", () => {
    expect(failures.get("assertion appended after evaluation")).toContain(
      "1 expect() chain built but never evaluated",
    );
    expect(rowsOf("assertion appended after evaluation")).toHaveLength(1);
  });

  it("passes after the appended assertion is re-evaluated", () => {
    expect(failures.get("appended assertion re-evaluated")).toBe("");
    expect(rowsOf("appended assertion re-evaluated")).toHaveLength(3);
  });

  it("leaves assertions appended during an in-flight evaluation unchecked", () => {
    expect(failures.get("assertion appended during evaluation")).toContain(
      "1 expect() chain built but never evaluated",
    );
    expect(rowsOf("assertion appended during evaluation")).toHaveLength(1);
  });

  it("does not carry unevaluated chains from a failed attempt into a retry", () => {
    expect(report.tests.find((test) => test.name.endsWith("guard resets for retry"))?.outcome)
      .toBe("passed");
    expect(rowsOf("guard resets for retry")).toHaveLength(1);
  });

  it("evaluates concurrent finish chains when scoped, otherwise fails with body guidance", () => {
    if ((vitest as Partial<typeof vitest>).aroundEach === undefined) {
      expect(failures.get("evaluates a chain in finish handler")).toContain(
        "Build the chain inside the test body, or upgrade to Vitest >=4.1",
      );
      expect(rowsOf("evaluates a chain in finish handler")).toHaveLength(0);
    } else {
      expect(failures.get("evaluates a chain in finish handler")).toBe("");
      expect(rowsOf("evaluates a chain in finish handler")).toHaveLength(1);
    }
  });

  it("does not let a late failure handler leave the next attempt unguarded", () => {
    expect(failures.get("late handler cannot exempt retry")).toContain(
      "1 expect() chain built but never evaluated",
    );
    expect(report.tests.find((test) => test.name.endsWith("late handler cannot exempt retry"))?.outcome)
      .toBe("failed");
  });

  it("fails only the concurrent test whose own chain was never evaluated", () => {
    expect(failures.get("concurrent never evaluated")).toContain(
      "1 expect() chain built but never evaluated",
    );
    expect(failures.get("concurrent evaluated chain")).toBe("");
    expect(report.tests.find((test) => test.name.endsWith("concurrent never evaluated"))?.outcome)
      .toBe("failed");
    expect(report.tests.find((test) => test.name.endsWith("concurrent evaluated chain"))?.outcome)
      .toBe("passed");
    expect(rowsOf("concurrent never evaluated")).toHaveLength(0);
    expect(rowsOf("concurrent evaluated chain")).toHaveLength(1);
  });

  it("counts every unevaluated chain of the test", () => {
    expect(failures.get("two never evaluated")).toContain("2 expect() chains built but never evaluated");
  });

  it("does not fail a test whose chain was evaluated", () => {
    expect(failures.get("evaluated chain")).toBe("");
  });

  it("fails a test that evaluates an empty chain", () => {
    expect(failures.get("empty chain")).toContain("expect() chain has no assertions");
  });

  it("computes row ids with the scheme-2 recipe, scope_id and spec_hash", () => {
    const [row] = rowsOf("evaluated chain");
    const testId = `${fixture} > evaluated chain`;
    const spec = { target: "output.message", check: "contains", value: "ok", case_sensitive: false, soft: false };
    expect(row).toMatchObject({ type: "content", scope_id: testId });
    expect(row!.assertion_id).toBe(assertionIdOf(testId, "content", spec, 0));
    expect(row!.spec_hash).toMatch(/^[0-9a-f]{64}$/);
  });

  it("gives two identical output_contains assertions distinct ids and reports both", () => {
    const rows = rowsOf("duplicate");
    const testId = `${fixture} > duplicate`;
    const spec = { target: "output.message", check: "contains", value: "ok", case_sensitive: false, soft: false };
    expect(rows.map((r) => r.assertion_id)).toEqual([
      assertionIdOf(testId, "content", spec, 0),
      assertionIdOf(testId, "content", spec, 1),
    ]);
    expect(rows.every((r) => r.status === "pass")).toBe(true);
  });

  it("keeps the hard fail and flags budget_exceeded when the soft-failure budget is breached", () => {
    const rows = rowsOf("budget breach with hard fail");
    expect(rows).toHaveLength(3);
    expect(rows.filter((r) => r.status === "hard_fail")).toHaveLength(1);
    expect(rows.filter((r) => r.status === "soft_fail")).toHaveLength(2);
    expect(report.budget_exceeded).toBe(true);
    expect(failures.get("budget breach with hard fail")).not.toBe("");
  });

  it("follows the plugin result for a plugin assertion while the judge in the batch evaluates", () => {
    const pass = rowsOf("plugin pass beside judge");
    expect(pass.map((r) => [r.type, r.status])).toEqual([
      ["plugin", "pass"],
      ["llm_judge", "pass"],
    ]);
    expect(pass[0]!.explanation).toBe("always-pass says pass");
    const fail = rowsOf("plugin fail beside judge");
    expect(fail.map((r) => [r.type, r.status])).toEqual([
      ["plugin", "hard_fail"],
      ["llm_judge", "pass"],
    ]);
    // The second identical judge request is served from the engine's judge cache.
    expect(judgeRequests).toBeGreaterThanOrEqual(1);
  });
});
