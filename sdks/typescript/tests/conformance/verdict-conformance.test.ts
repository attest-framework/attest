/**
 * Cross-SDK verdict fixtures (protocol-tests/verdicts): C1 aggregation.
 * The same fixtures run in the Python SDK and the Go engine.
 */
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { AgentResult } from "../../packages/core/src/result.js";
import type { AssertionResult } from "../../packages/core/src/proto/types.js";

interface VerdictFixture {
  name: string;
  results: AssertionResult[];
  expected: {
    outcome: string;
    strictOutcome: string;
    failedAssertionIds: string[];
    notEvaluated: Record<string, number>;
    passRate: number | null;
    softFailWarning: boolean;
  };
}

const dir = resolve(dirname(fileURLToPath(import.meta.url)), "../../../../protocol-tests/verdicts");
const fixtures: VerdictFixture[] = readdirSync(dir)
  .filter((f) => /^\d.*\.json$/.test(f))
  .sort()
  .map((f) => JSON.parse(readFileSync(join(dir, f), "utf8")) as VerdictFixture);

describe("verdict conformance fixtures", () => {
  it("loads every fixture", () => {
    expect(fixtures.length).toBeGreaterThanOrEqual(9);
  });

  it.each(fixtures.map((f) => [f.name, f] as const))("%s", (_name, fixture) => {
    const result = new AgentResult(
      { trace_id: "trc_verdict", output: { message: "x" }, steps: [] },
      fixture.results,
    );

    expect(result.outcome()).toBe(fixture.expected.outcome);
    expect(result.outcome({ strict: true })).toBe(fixture.expected.strictOutcome);
    expect(result.failedAssertions.map((r) => r.assertion_id)).toEqual(
      fixture.expected.failedAssertionIds,
    );
    expect(result.notEvaluatedReasons).toEqual(fixture.expected.notEvaluated);
    expect(result.passRate).toBe(fixture.expected.passRate);
    expect(result.softFailures.length > 0).toBe(fixture.expected.softFailWarning);
  });
});

describe("unknown status", () => {
  it("is never a pass", () => {
    const result = new AgentResult({ trace_id: "t", output: {}, steps: [] }, [
      { assertion_id: "x", status: "mystery", score: 1, explanation: "?" },
    ]);

    expect(result.assertionResults[0]?.status).toBe("not_evaluated");
    expect(result.outcome()).toBe("skip");
  });
});
