/**
 * End-to-end Vitest runs against the real engine: red means red.
 *
 * Runs tests/e2e/refund.verdicts.ts in a vitest subprocess with no provider
 * keys. Skipped when the engine binary has not been built (`make engine`).
 */
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const engine = resolve(pkgDir, "../../../../bin/attest-engine");

interface JsonAssertion {
  title: string;
  status: string;
  failureMessages: string[];
}

function runFixture(extraEnv: Record<string, string>, args: string[] = []) {
  const out = join(mkdtempSync(join(tmpdir(), "attest-vitest-")), "report.json");
  const env: Record<string, string> = {
    PATH: process.env["PATH"] ?? "",
    HOME: tmpdir(),
    COLUMNS: "200",
    ATTEST_ENGINE_PATH: engine,
    ATTEST_ENGINE_NO_DOWNLOAD: "1",
    ...extraEnv,
  };
  const proc = spawnSync(
    join(pkgDir, "node_modules/.bin/vitest"),
    [
      "run",
      "--config",
      "tests/e2e/vitest.e2e.config.ts",
      "--reporter=verbose",
      "--reporter=json",
      `--outputFile.json=${out}`,
      ...args,
    ],
    { cwd: pkgDir, env, encoding: "utf8" },
  );
  const report = JSON.parse(readFileSync(out, "utf8")) as {
    testResults: Array<{ assertionResults: JsonAssertion[] }>;
  };
  const byTitle = new Map<string, JsonAssertion>();
  for (const file of report.testResults) {
    for (const a of file.assertionResults) byTitle.set(a.title, a);
  }
  return { status: proc.status, stdout: proc.stdout, byTitle };
}

describe.skipIf(!existsSync(engine))("honest verdicts (real engine)", () => {
  it.each([
    ["no keys", {}, "provider_not_configured"],
    ["simulation", { ATTEST_SIMULATION: "1" }, "simulation"],
  ])("violated contracts fail and judge-only skips (%s)", (_name, extraEnv, reason) => {
    const run = runFixture(extraEnv);

    expect(run.status).toBe(1);
    for (const title of ["loop violated", "constraint violated", "content violated"]) {
      expect(run.byTitle.get(title)?.status).toBe("failed");
    }
    const loop = run.byTitle.get("loop violated")?.failureMessages.join("\n") ?? "";
    expect(loop).toContain("trace.loop_detection");
    expect(loop).toContain("not evaluated: short_circuited");
    expect(run.byTitle.get("judge only")?.status).toBe("skipped");
    expect(run.stdout).toContain(`judge only`);
    expect(run.stdout).toContain(`[not evaluated: ${reason}]`);
  }, 60_000);

  it("ATTEST_STRICT=1 fails a not-evaluated test", () => {
    const run = runFixture({ ATTEST_STRICT: "1" }, ["-t", "judge only"]);

    expect(run.status).toBe(1);
    const judge = run.byTitle.get("judge only");
    expect(judge?.status).toBe("failed");
    expect(judge?.failureMessages.join("\n")).toContain(
      "strict: not evaluated: provider_not_configured",
    );
  }, 60_000);
});
