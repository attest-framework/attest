/**
 * report-v2 builder (proto/attest/v1/report-v2.schema.json, contract C3).
 *
 * One row per assertion in `results`, each carrying the test id, file and line
 * of the test that requested it, and one row per test in `tests` with its
 * outcome. `attest policy evaluate` gates on `results`; the pr-annotate action
 * places inline comments with `file` and `line`.
 */
import {
  REASON_REPLAY_MISS,
  STATUS_HARD_FAIL,
  STATUS_NOT_EVALUATED,
  STATUS_PASS,
  STATUS_SOFT_FAIL,
  sumUsage,
  usageSummaryLine,
  zeroUsage,
  type AssertionResult,
  type Trace,
  type Usage,
} from "@attest-ai/core";

import type { ReplayMeta } from "./replay.js";
import { liveLine } from "@attest-ai/core";

export const REPORT_VERSION = 2;

export type TestOutcome = "passed" | "failed" | "skipped";

/** An assertion result as recorded for the report; `trial` is set under trials() with N > 1. */
export type RecordedResult = AssertionResult & {
  readonly trial?: number;
  /** Key into `AttestTaskMeta.traces`; set only when `ATTEST_REPORT_TRACES=1`. */
  readonly trace_id?: string;
};

/**
 * Set `ATTEST_REPORT_TRACES=1` to embed the evaluated traces in the report (`traces` and each
 * row's `trace_id`) for `attest view`. Off by default: traces hold prompts, model outputs and
 * tool arguments.
 */
export const REPORT_TRACES_ENV = "ATTEST_REPORT_TRACES";

export function reportTracesEnabled(): boolean {
  return (process.env[REPORT_TRACES_ENV] ?? "").trim() === "1";
}

/**
 * The report's `traces` map: one entry per distinct trace. Equal traces share an id; distinct
 * traces sharing a `trace_id` get `<id>#2`, `<id>#3`, ... and a trace without an id gets
 * `trace-<n>`, so a row's `trace_id` names exactly the trace it was evaluated against.
 */
export class TraceTable {
  constructor(readonly traces: Record<string, Trace> = {}) {}

  add(trace: Trace): string {
    const base = trace.trace_id === "" ? `trace-${Object.keys(this.traces).length + 1}` : trace.trace_id;
    const doc = JSON.stringify(trace);
    let key = base;
    for (let n = 1; key in this.traces; ) {
      if (JSON.stringify(this.traces[key]) === doc) return key;
      n += 1;
      key = `${base}#${n}`;
    }
    this.traces[key] = trace;
    return key;
  }
}

/** How a trials() call with N > 1 went. */
export interface TrialsMeta {
  /** N. */
  requested: number;
  /** Trials actually run; fewer than N when the test's cost budget was spent. */
  run: number;
  passed: number;
  /** Trials that passed, failed or raised (not counting not-evaluated ones). */
  evaluated: number;
  notEvaluated: number;
  stoppedByBudget: boolean;
}

/** Attest results a test recorded, carried from the worker in task meta. */
export interface AttestTaskMeta {
  /** `retryCount` of the attempt that produced `results`. */
  attempt: number;
  results: RecordedResult[];
  totalCost: number;
  totalDurationMs: number;
  /** Attest model usage of every attempt of the test, including retried ones. */
  usage: Usage;
  trials?: TrialsMeta;
  /** Traces the results were evaluated against, by `trace_id` (only with `ATTEST_REPORT_TRACES=1`). */
  traces?: Record<string, Trace>;
}

declare module "vitest" {
  interface TaskMeta {
    attest?: AttestTaskMeta;
    attestReplay?: ReplayMeta;
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
  replay?: ReplayMeta;
}

/**
 * `unattached` is the usage of evaluations that belong to no test (beforeAll,
 * scripts); it counts in `summary.usage`.
 */
export function buildReport(
  tests: readonly TestRecord[],
  simulated: boolean,
  unattached: Usage = zeroUsage(),
  includeTraces = false,
): Record<string, unknown> {
  const table = includeTraces ? new TraceTable() : undefined;
  const rows: Record<string, unknown>[] = [];
  const testRows: Record<string, unknown>[] = [];
  let totalCost = 0;
  let totalDurationMs = 0;
  for (const test of tests) {
    if (test.line < 1) throw new Error(`${test.testId}: line must be >= 1, got ${test.line}`);
    const assertionIds = new Set<string>();
    const reasons: string[] = [];
    totalCost += test.attest?.totalCost ?? 0;
    totalDurationMs += test.attest?.totalDurationMs ?? 0;
    // The worker keyed traces per test; re-key them into the report-wide table.
    const traceKeys = new Map<string, string>();
    for (const [local, trace] of Object.entries(test.attest?.traces ?? {})) {
      if (table !== undefined) traceKeys.set(local, table.add(trace));
    }
    for (const result of test.attest?.results ?? []) {
      const { trace_id: local, ...rest } = result;
      const traceId = local === undefined ? undefined : traceKeys.get(local);
      rows.push({
        ...rest,
        ...(traceId === undefined ? {} : { trace_id: traceId }),
        test_id: test.testId,
        file: test.file,
        line: test.line,
      });
      assertionIds.add(result.assertion_id);
      if (result.status === STATUS_NOT_EVALUATED && !reasons.includes(String(result.reason))) {
        reasons.push(String(result.reason));
      }
    }
    const replay = test.replay;
    if (replay?.notEvaluated === true) {
      rows.push({
        assertion_id: `replay_${replay.fixtures[0] ?? "fixture"}`,
        status: STATUS_NOT_EVALUATED,
        reason: REASON_REPLAY_MISS,
        score: 0,
        explanation: replay.miss ?? "replay miss",
        cost: 0,
        duration_ms: 0,
        test_id: test.testId,
        file: test.file,
        line: test.line,
      });
      assertionIds.add(`replay_${replay.fixtures[0] ?? "fixture"}`);
      if (!reasons.includes(REASON_REPLAY_MISS)) reasons.push(REASON_REPLAY_MISS);
    }
    testRows.push({
      test_id: test.testId,
      name: test.name,
      file: test.file,
      line: test.line,
      outcome: test.outcome,
      ...(reasons.length > 0 ? { not_evaluated_reasons: reasons } : {}),
      assertion_ids: [...assertionIds],
      duration_ms: Math.round(test.durationMs),
      ...(test.attest?.trials !== undefined ? { trials: test.attest.trials.run } : {}),
      ...(replay !== undefined
        ? {
            replay: {
              fixtures: replay.fixtures,
              mode: replay.mode,
              replayed: replay.replayed,
              live: replay.live,
              misses: replay.misses,
              ...(replay.miss !== undefined ? { miss: replay.miss } : {}),
            },
          }
        : {}),
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
      usage: sessionUsage(tests, unattached),
      ...replaySummary(tests),
    },
    results: rows,
    tests: testRows,
    total_cost: totalCost,
    total_duration_ms: totalDurationMs,
    ...(table === undefined ? {} : { traces: table.traces }),
  };
}

/** `summary.replay` for the tests that ran in replay mode; absent when none did. */
function replaySummary(tests: readonly TestRecord[]): { replay?: Record<string, number> } {
  const replays = tests.flatMap((t) => (t.replay === undefined ? [] : [t.replay]));
  if (replays.length === 0) return {};
  const sum = (f: (r: ReplayMeta) => number) => replays.reduce((n, r) => n + f(r), 0);
  return {
    replay: {
      replayed: sum((r) => r.replayed),
      live: sum((r) => r.live),
      misses: sum((r) => r.misses),
      live_cost_usd: sum((r) => r.liveCostUsd),
      unpriced_live_calls: sum((r) => r.unpricedLiveCalls),
    },
  };
}

/** Attest usage summed over every test of the run plus the evaluations that belong to no test. */
export function sessionUsage(tests: readonly TestRecord[], unattached: Usage = zeroUsage()): Usage {
  return sumUsage([unattached, ...tests.flatMap((t) => (t.attest === undefined ? [] : [t.attest.usage]))]);
}

/**
 * Lines for the end of the run: one per trials test
 * ("<test_id>: <passed>/<evaluated> trials passed (<n> not evaluated)", plus a
 * note when the test budget stopped it early), then the "attest usage: …" line
 * when the session made or refused a model call.
 */
export function summaryLines(tests: readonly TestRecord[], unattached: Usage = zeroUsage()): string[] {
  const lines: string[] = [];
  for (const test of tests) {
    const trials = test.attest?.trials;
    if (trials === undefined) continue;
    lines.push(
      `${test.testId}: ${trials.passed}/${trials.evaluated} trials passed (${trials.notEvaluated} not evaluated)`,
    );
    if (trials.stoppedByBudget) {
      lines.push(
        `${test.testId}: stopped after ${trials.run} of ${trials.requested} trials: test budget spent`,
      );
    }
  }
  const usage = usageSummaryLine(sessionUsage(tests, unattached));
  if (usage !== undefined) lines.push(usage);
  const replays = tests.flatMap((t) => (t.replay === undefined ? [] : [t.replay]));
  if (replays.length > 0) {
    const n = (f: (r: ReplayMeta) => number) => replays.reduce((a, r) => a + f(r), 0);
    lines.push(
      `attest replay: ${String(n((r) => r.replayed))} replayed · ` +
        liveLine(n((r) => r.live), n((r) => r.liveCostUsd), n((r) => r.unpricedLiveCalls)) +
        (n((r) => r.misses) > 0 ? ` · ${String(n((r) => r.misses))} missed` : ""),
    );
  }
  return lines;
}
