/**
 * Cross-SDK verdict fixtures (protocol-tests/verdicts): outcome aggregation.
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

interface VerdictSchema {
  required: string[];
  properties: {
    results: {
      items: {
        required: string[];
        properties: { status: { enum: string[] }; reason: { enum: string[] } };
      };
    };
    expected: { required: string[] };
  };
}

const schema = JSON.parse(
  readFileSync(join(dir, "verdict.schema.json"), "utf8"),
) as VerdictSchema;

/**
 * Checks the rules verdict.schema.json states (required keys, closed key
 * sets, status/reason enums, reason only and always on not_evaluated rows,
 * no failure_class on them). The schema is read, not restated, so a schema
 * change shows up here. Python validates with a full JSON Schema engine.
 */
function schemaViolations(raw: Record<string, unknown>): string[] {
  const out: string[] = [];
  const top = schema.required;
  const expectedKeys = schema.properties.expected.required;
  const item = schema.properties.results.items;
  for (const key of top) if (!(key in raw)) out.push(`missing ${key}`);
  for (const key of Object.keys(raw)) if (!top.includes(key)) out.push(`unexpected ${key}`);
  const exp = (raw["expected"] ?? {}) as Record<string, unknown>;
  for (const key of expectedKeys) if (!(key in exp)) out.push(`expected.${key} missing`);
  for (const key of Object.keys(exp)) {
    if (!expectedKeys.includes(key)) out.push(`expected.${key} unexpected`);
  }
  const rows = (raw["results"] ?? []) as Array<Record<string, unknown>>;
  if (rows.length === 0) out.push("results empty");
  for (const row of rows) {
    const id = String(row["assertion_id"]);
    for (const key of item.required) if (!(key in row)) out.push(`${id}: missing ${key}`);
    if (!item.properties.status.enum.includes(String(row["status"]))) {
      out.push(`${id}: status ${String(row["status"])}`);
    }
    if (row["status"] === "not_evaluated") {
      if (!item.properties.reason.enum.includes(String(row["reason"]))) {
        out.push(`${id}: reason ${String(row["reason"])}`);
      }
      if ("failure_class" in row) out.push(`${id}: failure_class on not_evaluated`);
    } else if ("reason" in row) {
      out.push(`${id}: reason on ${String(row["status"])}`);
    }
  }
  return out;
}

describe("verdict conformance fixtures", () => {
  it("loads every fixture", () => {
    expect(fixtures.length).toBeGreaterThanOrEqual(9);
  });

  it.each(fixtures.map((f) => [f.name, f] as const))("%s matches verdict.schema.json", (_n, f) => {
    expect(schemaViolations(f as unknown as Record<string, unknown>)).toEqual([]);
  });

  it("schema check rejects a reason on a non-not_evaluated row", () => {
    const bad = structuredClone(fixtures[0]) as unknown as Record<string, unknown>;
    (bad["results"] as Array<Record<string, unknown>>)[0]!["reason"] = "simulation";

    expect(schemaViolations(bad)).toContain("a1: reason on pass");
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
