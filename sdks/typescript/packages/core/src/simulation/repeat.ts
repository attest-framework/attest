import { OUTCOME_PASS, OUTCOME_SKIP, type Outcome } from "../proto/constants.js";
import {
  aggregateOutcomes,
  formatSkipReason,
  sumNotEvaluatedReasons,
  verdictsDiagnostics,
  type Verdict,
} from "./verdict.js";

/** Results of `n` trials, aggregated under the assertion-status rules. */
export class RepeatResult implements Verdict {
  readonly results: readonly Verdict[];

  constructor(results: readonly Verdict[] = []) {
    this.results = results;
  }

  get count(): number {
    return this.results.length;
  }

  /** Trials skipped because an assertion was not evaluated. */
  get notEvaluated(): number {
    return this.results.filter((r) => r.outcome() === OUTCOME_SKIP).length;
  }

  /** Passing / evaluated trials; null when no trial was evaluated. */
  get passRate(): number | null {
    const evaluated = this.count - this.notEvaluated;
    if (evaluated === 0) return null;
    return this.results.filter((r) => r.outcome() === OUTCOME_PASS).length / evaluated;
  }

  /** True when every trial's outcome is pass (soft failures included). */
  get allPassed(): boolean {
    return this.results.every((r) => r.outcome() === OUTCOME_PASS);
  }

  outcome(options?: { strict?: boolean }): Outcome {
    return aggregateOutcomes(this.results.map((r) => r.outcome(options)));
  }

  get notEvaluatedReasons(): Readonly<Record<string, number>> {
    return sumNotEvaluatedReasons(this.results);
  }

  skipReason(): string {
    return formatSkipReason(this.notEvaluatedReasons);
  }

  renderDiagnostics(options?: { color?: boolean }): string {
    return verdictsDiagnostics(this.results, "trial", options?.color);
  }
}

/** Run `fn` `n` times sequentially and collect the verdicts. */
export function repeat<A extends unknown[]>(
  n: number,
  fn: (...args: A) => Verdict | Promise<Verdict>,
): (...args: A) => Promise<RepeatResult> {
  return async (...args: A): Promise<RepeatResult> => {
    const results: Verdict[] = [];
    for (let i = 0; i < n; i++) {
      results.push(await fn(...args));
    }
    return new RepeatResult(results);
  };
}
