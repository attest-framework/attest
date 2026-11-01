/**
 * report-v2 builder (proto/attest/v1/report-v2.schema.json, contract C3).
 *
 * One row per assertion in `results`, each carrying the test id, file and line
 * of the test that requested it, and one row per test in `tests` with its
 * outcome. `attest policy evaluate` gates on `results`; the pr-annotate action
 * places inline comments with `file` and `line`.
 */
import {
  STATUS_HARD_FAIL,
  STATUS_NOT_EVALUATED,
  STATUS_PASS,
  STATUS_SOFT_FAIL,
  type AssertionResult,
} from "@attest-ai/core";

export const REPORT_VERSION = 2;

export type TestOutcome = "passed" | "failed" | "skipped";

/** Attest results a test recorded, carried from the worker in task meta. */
export interface AttestTaskMeta {
  /** `retryCount` of the attempt that produced `results`. */
  attempt: number;
  results: AssertionResult[];
  totalCost: number;
  totalDurationMs: number;
}

declare module "vitest" {
  interface TaskMeta {
    attest?: AttestTaskMeta;
  }
}

export interface TestRecord {
  testId: string;
  name: string;
  /** Relative to the Vitest root, forward slashes. */
  file: string;
  /** 1-based line of the test definition. */
  line: number;
  outcome: TestOutcome;
  durationMs: number;
  attest: AttestTaskMeta | undefined;
}

export function buildReport(tests: readonly TestRecord[], simulated: boolean): Record<string, unknown> {
  const rows: Record<string, unknown>[] = [];
  const testRows: Record<string, unknown>[] = [];
  let totalCost = 0;
  let totalDurationMs = 0;
  for (const test of tests) {
    if (test.line < 1) throw new Error(`${test.testId}: line must be >= 1, got ${test.line}`);
    const assertionIds: string[] = [];
    const reasons: string[] = [];
    totalCost += test.attest?.totalCost ?? 0;
    totalDurationMs += test.attest?.totalDurationMs ?? 0;
    for (const result of test.attest?.results ?? []) {
      rows.push({ ...result, test_id: test.testId, file: test.file, line: test.line });
      assertionIds.push(result.assertion_id);
      if (result.status === STATUS_NOT_EVALUATED && !reasons.includes(String(result.reason))) {
        reasons.push(String(result.reason));
      }
    }
    testRows.push({
      test_id: test.testId,
      name: test.name,
      file: test.file,
      line: test.line,
      outcome: test.outcome,
      ...(reasons.length > 0 ? { not_evaluated_reasons: reasons } : {}),
      assertion_ids: assertionIds,
      duration_ms: Math.round(test.durationMs),
    });
  }
  const count = (status: string) => rows.filter((r) => r["status"] === status).length;
  return {
    report_version: REPORT_VERSION,
    timestamp: new Date().toISOString().replace(/\.\d{3}Z$/, "Z"),
    simulated,
    summary: {
      total: rows.length,
      passed: count(STATUS_PASS),
      soft_fail: count(STATUS_SOFT_FAIL),
      hard_fail: count(STATUS_HARD_FAIL),
      not_evaluated: count(STATUS_NOT_EVALUATED),
    },
    results: rows,
    tests: testRows,
    total_cost: totalCost,
    total_duration_ms: totalDurationMs,
  };
}
