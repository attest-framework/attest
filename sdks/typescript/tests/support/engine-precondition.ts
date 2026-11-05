/**
 * Vitest globalSetup: engine-backed tests need a current `bin/attest-engine`.
 * A missing or stale binary fails the run once, here, with the remedy, instead
 * of failing every engine-backed test separately.
 */
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const REMEDY = "run `make engine` from the repository root";

export type EngineVersionProbe = (binary: string) => string;

function probeVersion(binary: string): string {
  const result = spawnSync(binary, ["version"], { encoding: "utf8", timeout: 10_000 });
  if (result.error || result.status !== 0) {
    const reason = result.error?.message ?? `exit status ${result.status}: ${result.stderr.trim()}`;
    throw new Error(`could not run '${binary} version' (${reason}); ${REMEDY}`);
  }
  return result.stdout.trim().replace(/^attest-engine\s+/, "");
}

/** Throws unless `<repoRoot>/bin/attest-engine` exists and reports the version in `<repoRoot>/VERSION`. */
export function assertEngineCurrent(repoRoot: string, probe: EngineVersionProbe = probeVersion): void {
  const binary = join(repoRoot, "bin", "attest-engine");
  if (!existsSync(binary)) {
    throw new Error(`engine-backed tests need ${binary}, which is missing: ${REMEDY}`);
  }
  const expected = readFileSync(join(repoRoot, "VERSION"), "utf8").trim();
  const actual = probe(binary);
  if (actual !== expected) {
    throw new Error(
      `${binary} is stale: it reports version ${actual}, VERSION is ${expected}: ${REMEDY}`,
    );
  }
}

export default function setup(): void {
  const here = dirname(fileURLToPath(import.meta.url));
  assertEngineCurrent(resolve(here, "../../../.."));
}
