import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");

it("never silently drops chains when a later file reuses the cached integration", () => {
  const dir = mkdtempSync(join(tmpdir(), "attest-cached-guard-"));
  try {
    const jsonPath = join(dir, "run.json");
    const run = spawnSync(join(pkgDir, "node_modules/.bin/vitest"), [
      "run", "--config", "tests/e2e/vitest.guard-cached.config.ts", `--outputFile=${jsonPath}`,
    ], {
      cwd: pkgDir,
      env: { PATH: process.env["PATH"] ?? "", HOME: tmpdir() },
      encoding: "utf8",
      timeout: 30_000,
    });
    expect(run.error).toBeUndefined();
    expect(run.status, run.stdout + run.stderr).toBe(1);
    const result = JSON.parse(readFileSync(jsonPath, "utf8")) as {
      testResults: Array<{ assertionResults: Array<{
        title: string;
        status: string;
        failureMessages: string[];
      }> }>;
    };
    const tests = result.testResults.flatMap((file) => file.assertionResults);
    expect(tests).toHaveLength(4);
    for (const test of tests) {
      expect(test.status, test.title).toBe("failed");
      expect(test.failureMessages.join("\n")).toMatch(
        /1 expect\(\) chain built but never evaluated|Attest cannot bind this concurrent test's chain guard/,
      );
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}, 60_000);
