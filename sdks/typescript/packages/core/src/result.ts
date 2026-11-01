import type { AssertionResult, Trace } from "./proto/types.js";
import {
  STATUS_PASS,
  STATUS_SOFT_FAIL,
  STATUS_HARD_FAIL,
  STATUS_NOT_EVALUATED,
  OUTCOME_FAIL,
  OUTCOME_PASS,
  OUTCOME_SKIP,
  type Outcome,
} from "./proto/constants.js";

const KNOWN_STATUSES: ReadonlySet<string> = new Set([
  STATUS_PASS,
  STATUS_SOFT_FAIL,
  STATUS_HARD_FAIL,
  STATUS_NOT_EVALUATED,
]);

/**
 * Normalize an engine result row. Protocol rule: an unknown status is
 * never a pass, so it becomes "not_evaluated".
 */
export function normalizeAssertionResult(r: AssertionResult): AssertionResult {
  if (KNOWN_STATUSES.has(r.status)) return r;
  return {
    ...r,
    status: STATUS_NOT_EVALUATED,
    explanation: `[unknown status '${r.status}'] ${r.explanation}`,
  };
}

export class AgentResult {
  readonly trace: Trace;
  readonly assertionResults: readonly AssertionResult[];
  readonly totalCost: number;
  readonly totalDurationMs: number;

  constructor(
    trace: Trace,
    assertionResults: readonly AssertionResult[] = [],
    totalCost = 0.0,
    totalDurationMs = 0,
  ) {
    this.trace = trace;
    this.assertionResults = assertionResults.map(normalizeAssertionResult);
    this.totalCost = totalCost;
    this.totalDurationMs = totalDurationMs;
  }

  /** True if every assertion has status "pass". */
  get passed(): boolean {
    return this.assertionResults.every((r) => r.status === STATUS_PASS);
  }

  /**
   * Test outcome after the whole batch. Any hard_fail fails; otherwise any
   * not_evaluated skips (fails when `strict`); otherwise pass, including
   * when only soft_fail rows are present (see `softFailures`).
   */
  outcome(options?: { strict?: boolean }): Outcome {
    if (this.hardFailures.length > 0) return OUTCOME_FAIL;
    if (this.notEvaluated.length > 0) {
      return options?.strict === true ? OUTCOME_FAIL : OUTCOME_SKIP;
    }
    return OUTCOME_PASS;
  }

  /** Failed assertions: hard_fail and soft_fail only. */
  get failedAssertions(): readonly AssertionResult[] {
    return this.assertionResults.filter(
      (r) => r.status === STATUS_HARD_FAIL || r.status === STATUS_SOFT_FAIL,
    );
  }

  get hardFailures(): readonly AssertionResult[] {
    return this.assertionResults.filter((r) => r.status === STATUS_HARD_FAIL);
  }

  get softFailures(): readonly AssertionResult[] {
    return this.assertionResults.filter((r) => r.status === STATUS_SOFT_FAIL);
  }

  /** Assertions the engine did not evaluate; each carries a `reason`. */
  get notEvaluated(): readonly AssertionResult[] {
    return this.assertionResults.filter((r) => r.status === STATUS_NOT_EVALUATED);
  }

  /** Count of not_evaluated assertions per reason, in first-seen order. */
  get notEvaluatedReasons(): Readonly<Record<string, number>> {
    const counts: Record<string, number> = {};
    for (const r of this.notEvaluated) {
      const key = r.reason ?? "unknown";
      counts[key] = (counts[key] ?? 0) + 1;
    }
    return counts;
  }

  /**
   * Passed / evaluated assertions; null when nothing was evaluated.
   * not_evaluated rows are excluded from the denominator.
   */
  get passRate(): number | null {
    const evaluated = this.assertionResults.length - this.notEvaluated.length;
    return evaluated === 0 ? null : this.passCount / evaluated;
  }

  get passCount(): number {
    return this.assertionResults.filter((r) => r.status === STATUS_PASS).length;
  }

  /** Number of failed assertions (hard_fail + soft_fail). */
  get failCount(): number {
    return this.failedAssertions.length;
  }

  /** "not evaluated: <reason>[, <reason> ×n ...]" for skipped tests. */
  skipReason(): string {
    const parts = Object.entries(this.notEvaluatedReasons).map(([reason, n]) =>
      n === 1 ? reason : `${reason} ×${n}`,
    );
    return `not evaluated: ${parts.join(", ")}`;
  }
}
