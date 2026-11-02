/**
 * Fixed-N trials: the Vitest counterpart of pytest's `--attest-trials=N`.
 */
import type { TestContext } from "vitest";
import {
  AgentResult,
  FailureClass,
  STATUS_HARD_FAIL,
  STATUS_NOT_EVALUATED,
  renderDiagnostics,
} from "@attest-ai/core";
import type { RecordedResult, TrialsMeta } from "./report.js";
import {
  attemptMeta,
  attemptState,
  costBudgetSpent,
  runningTest,
  strictFromEnv,
  trialScope,
  validateBudget,
  type RunningTest,
  type TestBudget,
} from "./test-state.js";

/** How attestGlobalSetup({ minPassRate }) reaches the workers that run trials(). */
export const MIN_PASS_RATE_ENV = "ATTEST_MIN_PASS_RATE";

export interface TrialsOptions {
  /** Number of trials N; defaults to the ATTEST_TRIALS environment variable, else 1. */
  n?: number;
  /**
   * The running test's context (`it("…", async (context) => …)`); required under
   * `test.concurrent` and on Vitest before 4.1.
   */
  context?: TestContext;
  /**
   * Limits for the test's attest model calls across all trials; see
   * InspectOptions.testBudget. Before each trial after the first, the trials stop
   * when attest cost plus agent spend has reached `maxCostUsd`.
   */
  budget?: TestBudget;
  /**
   * Fail the test when passed / evaluated trials is below this rate (0..1).
   * Overrides the run's value: attestGlobalSetup({ minPassRate }) or the
   * ATTEST_MIN_PASS_RATE environment variable, else 1.0 (any failing trial fails).
   */
  minPassRate?: number;
}

type TrialOutcome = "pass" | "fail" | "skip";

interface TrialRecord {
  index: number;
  results: AgentResult[];
  /** What the trial threw, when it threw. */
  error: { value: unknown } | undefined;
}

function trialCount(n: number | undefined): number {
  const raw = n ?? Number(process.env["ATTEST_TRIALS"] ?? 1);
  if (!Number.isInteger(raw) || raw < 1) {
    const source = n === undefined ? `ATTEST_TRIALS=${process.env["ATTEST_TRIALS"]}` : `n=${n}`;
    throw new Error(`trials(): N must be an integer >= 1, got ${source}`);
  }
  return raw;
}

/** A pass rate in [0, 1]; anything else is an error naming where it came from. */
export function validateMinPassRate(rate: number, source: string): number {
  if (!Number.isFinite(rate) || rate < 0 || rate > 1) {
    throw new Error(`minPassRate must be a number between 0 and 1, got ${source}`);
  }
  return rate;
}

/** The run's minimum pass rate: ATTEST_MIN_PASS_RATE (set by attestGlobalSetup), else 1.0. */
function runMinPassRate(): number {
  const raw = process.env[MIN_PASS_RATE_ENV];
  if (raw === undefined) return 1;
  return validateMinPassRate(raw.trim() === "" ? Number.NaN : Number(raw), `${MIN_PASS_RATE_ENV}=${raw}`);
}

function describeError(error: unknown): string {
  return error instanceof Error ? `${error.name}: ${error.message}` : `${typeof error}: ${String(error)}`;
}

/** A trial's own outcome: raised or any hard_fail fails; else not_evaluated (or nothing evaluated) skips. */
function trialOutcome(trial: TrialRecord): TrialOutcome {
  if (trial.error !== undefined) return "fail";
  const rows = trial.results.flatMap((r) => r.assertionResults);
  if (rows.some((r) => r.status === STATUS_HARD_FAIL)) return "fail";
  if (rows.length === 0) return "skip";
  if (rows.some((r) => r.status === STATUS_NOT_EVALUATED)) return "skip";
  return "pass";
}

/**
 * Replace the rows of each trial that threw with exactly one hard_fail row per
 * assertion ID of the test, so pass rates count the trial as failed once per ID.
 * When the trial had evaluated the ID before throwing, the explanation keeps
 * the engine's status.
 */
function thrownTrialRows(test: RunningTest, trials: readonly TrialRecord[]): void {
  const meta = attemptMeta(test);
  const ids = [...new Set(trials.flatMap((t) => t.results.flatMap((r) => r.assertionResults)).map((r) => r.assertion_id))];
  for (const trial of trials) {
    if (trial.error === undefined) continue;
    const before = new Map(
      trial.results.flatMap((r) => r.assertionResults.map((row) => [row.assertion_id, row.status] as const)),
    );
    meta.results = meta.results.filter((row) => row.trial !== trial.index);
    const raised = `trial ${trial.index} raised ${describeError(trial.error.value)}`;
    for (const id of ids) {
      const status = before.get(id);
      const row: RecordedResult = {
        assertion_id: id,
        status: STATUS_HARD_FAIL,
        score: 0,
        explanation: status === undefined ? raised : `${raised} (engine status before the error: ${status})`,
        failure_class: FailureClass.BrokenCode,
        trial: trial.index,
      };
      meta.results.push(row);
    }
  }
}

function failureMessage(summary: TrialsMeta, trials: readonly TrialRecord[], minPassRate: number): string {
  const rate = summary.passed / summary.evaluated;
  const parts = [
    `${summary.passed}/${summary.evaluated} trials passed (pass rate ${rate.toFixed(2)}) ` +
      `below min pass rate ${minPassRate.toFixed(2)}`,
  ];
  for (const trial of trials) {
    if (trialOutcome(trial) !== "fail") continue;
    if (trial.error !== undefined) {
      parts.push(`trial ${trial.index} raised ${describeError(trial.error.value)}`);
      continue;
    }
    for (const result of trial.results) {
      if (result.hardFailures.length === 0) continue;
      parts.push(`trial ${trial.index}:${renderDiagnostics(result)}`);
    }
  }
  return parts.join("\n");
}

/**
 * Run `body` N times inside the current test (N = `options.n`, else the
 * ATTEST_TRIALS environment variable, else 1) and enforce only the aggregate.
 *
 * With N = 1 the body runs once as a plain test body: evaluate() enforces as
 * usual. With N > 1, during trial i (1-based): evaluate() does not enforce
 * (it behaves as inspect()), enforceOutcome() returns its argument, every
 * result row is reported with `trial = i`, and assertion IDs restart so each
 * assertion keeps its ID across trials. A trial that throws counts as failed:
 * its rows are replaced by one hard_fail row (failure_class broken_code) per
 * assertion ID the test evaluated; when no trial evaluated anything, the first
 * error is rethrown. A trial whose own outcome is skip counts as not evaluated,
 * also under ATTEST_STRICT. After the last trial the test is skipped when no trial was
 * evaluated (failed under ATTEST_STRICT), fails when passed / evaluated
 * trials is below the minimum pass rate (default 1.0: any failing trial
 * fails; not-evaluated trials are left out of the rate), and passes otherwise. AttestReporter prints "<test_id>: <passed>/<evaluated>
 * trials passed (<n> not evaluated)".
 *
 * This is a pass/fail gate on N runs, not a statistical verdict: whether the
 * pass rate regressed (pass / regressed / inconclusive) comes from
 * `attest policy evaluate --baseline <tag>` with a `regression` rule over the
 * report's per-trial rows.
 */
export async function trials(
  body: (trial: number) => Promise<void> | void,
  options?: TrialsOptions,
): Promise<void> {
  const n = trialCount(options?.n);
  const minPassRate =
    options?.minPassRate === undefined
      ? runMinPassRate()
      : validateMinPassRate(options.minPassRate, `minPassRate=${options.minPassRate}`);
  const budget = options?.budget === undefined ? undefined : validateBudget(options.budget);
  const test = options?.context?.task ?? (await runningTest());
  if (test === undefined) throw new Error("trials() must be called inside a test");
  if (options?.context === undefined && test.concurrent === true) {
    throw new Error("trials() in a test.concurrent test — pass the test context: trials(body, { context })");
  }
  if (n === 1) {
    await trialScope.run({ test, trial: undefined, budget, results: [] }, () => body(1));
    return;
  }

  const state = attemptState(test);
  const records: TrialRecord[] = [];
  let stoppedByBudget = false;
  for (let index = 1; index <= n; index++) {
    if (index > 1 && costBudgetSpent(budget, state)) {
      stoppedByBudget = true;
      break;
    }
    state.ordinal = 0;
    const record: TrialRecord = { index, results: [], error: undefined };
    records.push(record);
    try {
      await trialScope.run({ test, trial: index, budget, results: record.results }, () => body(index));
    } catch (error) {
      record.error = { value: error };
    }
  }

  const evaluatedAny = records.some((r) => r.results.some((res) => res.assertionResults.length > 0));
  const firstError = records.find((r) => r.error !== undefined)?.error;
  if (!evaluatedAny && firstError !== undefined) throw firstError.value;
  thrownTrialRows(test, records);

  const strict = strictFromEnv();
  const outcomes = records.map((r) => trialOutcome(r));
  const passed = outcomes.filter((o) => o === "pass").length;
  const failed = outcomes.filter((o) => o === "fail").length;
  const summary: TrialsMeta = {
    requested: n,
    run: records.length,
    passed,
    evaluated: passed + failed,
    notEvaluated: outcomes.length - passed - failed,
    stoppedByBudget,
  };
  attemptMeta(test).trials = summary;

  if (summary.evaluated === 0) {
    const all = records.flatMap((r) => r.results);
    const rows = all.flatMap((r) => r.assertionResults);
    const first = all[0];
    const reason =
      first === undefined || rows.length === 0
        ? `not evaluated: no assertions in ${records.length} trials`
        : new AgentResult(first.trace, rows).skipReason();
    if (strict) throw new Error(`strict: ${reason}`);
    (options?.context ?? test.context).skip(reason);
    return;
  }
  if (passed / summary.evaluated < minPassRate) {
    throw new Error(failureMessage(summary, records, minPassRate));
  }
}
