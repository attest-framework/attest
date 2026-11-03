/**
 * The Vitest files `attest capture` drafts run green against the real engine: the
 * incident reproduction drafted from the INC-1042 fixture reports the double refund
 * (inspect), and the test drafted from a verified fixture enforces its contracts
 * (evaluate).
 * Skipped when the engine binary has not been built (`make engine`).
 */
import { spawnSync } from "node:child_process";
import { copyFileSync, existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { emitVitest } from "@attest-ai/core";
import { afterAll, describe, expect, it } from "vitest";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const engine = resolve(pkgDir, "../../../../bin/attest-engine");
const incidentOtlp = resolve(pkgDir, "../../../../protocol-tests/trace-v2/otlp/inc-1042-refund.otlp.json");
const verifiedFixture = resolve(pkgDir, "../../../../protocol-tests/fixture-v1/valid/004-verified-schema-and-cost.attest.json");

describe.skipIf(!existsSync(engine))("drafted Vitest file (real engine)", () => {
  const dir = mkdtempSync(join(pkgDir, "tests", "e2e", ".capture-"));

  afterAll(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it("reproduces INC-1042 and enforces a verified fixture", () => {
    const capture = spawnSync(
      engine,
      ["capture", "--name", "inc-1042", "--expect-fail", "--from", incidentOtlp, "--out-dir", join(dir, "fixtures")],
      { encoding: "utf8" },
    );
    expect(capture.status, capture.stderr + capture.stdout).toBe(0);
    const testFile = emitVitest(join(dir, "fixtures", "inc-1042.attest.json"));
    expect(testFile).toBe(join(dir, "inc-1042.test.ts"));
    copyFileSync(verifiedFixture, join(dir, "fixtures", "refund-verified.attest.json"));
    expect(emitVitest(join(dir, "fixtures", "refund-verified.attest.json"))).toBe(join(dir, "refund-verified.test.ts"));
    expect(readFileSync(join(dir, "refund-verified.test.ts"), "utf8")).toContain("await evaluate(contracts());");

    const out = join(mkdtempSync(join(tmpdir(), "attest-vitest-")), "report.json");
    const run = spawnSync(
      join(pkgDir, "node_modules/.bin/vitest"),
      ["run", "--config", "tests/e2e/vitest.capture.config.ts", "--reporter=json", `--outputFile.json=${out}`],
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

    expect(run.status, run.stdout + run.stderr).toBe(0);
    const report = JSON.parse(readFileSync(out, "utf8")) as { numPassedTests: number; numTotalTests: number };
    expect(report).toMatchObject({ numPassedTests: 2, numTotalTests: 2 });
  }, 60_000);
});
