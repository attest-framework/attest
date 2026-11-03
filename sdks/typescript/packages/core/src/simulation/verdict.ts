import { OUTCOME_FAIL, OUTCOME_PASS, OUTCOME_SKIP, type Outcome } from "../proto/constants.js";
import { renderDiagnostics } from "../diagnostics.js";
import { AgentResult } from "../result.js";

/**
 * Anything with a test outcome: `AgentResult`, `RepeatResult` and
 * `ScenarioResult`. Aggregates nest.
 */
export interface Verdict {
  readonly notEvaluatedReasons: Readonly<Record<string, number>>;
  outcome(options?: { strict?: boolean }): Outcome;
  skipReason(): string;
}

/** Any failing outcome fails; otherwise any skip skips; otherwise pass. */
export function aggregateOutcomes(outcomes: readonly Outcome[]): Outcome {
  if (outcomes.includes(OUTCOME_FAIL)) return OUTCOME_FAIL;
  if (outcomes.includes(OUTCOME_SKIP)) return OUTCOME_SKIP;
  return OUTCOME_PASS;
}

/** Not-evaluated reasons summed across verdicts, in first-seen order. */
export function sumNotEvaluatedReasons(results: readonly Verdict[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const r of results) {
    for (const [reason, n] of Object.entries(r.notEvaluatedReasons)) {
      counts[reason] = (counts[reason] ?? 0) + n;
    }
  }
  return counts;
}

export function formatSkipReason(counts: Readonly<Record<string, number>>): string {
  const parts = Object.entries(counts).map(([reason, n]) => (n === 1 ? reason : `${reason} ×${n}`));
  return `not evaluated: ${parts.join(", ")}`;
}

/** Diagnostics of every verdict whose outcome is not pass, headed by its index. */
export function verdictsDiagnostics(
  results: readonly (Verdict & { renderDiagnostics?(options?: { color?: boolean }): string })[],
  label: string,
  color = false,
): string {
  const blocks = results.flatMap((r, i) => {
    if (r.outcome() === OUTCOME_PASS) return [];
    const body =
      r instanceof AgentResult
        ? renderDiagnostics(r, { color })
        : (r.renderDiagnostics?.({ color }) ?? "");
    return [`${label} ${i + 1}/${results.length}:${body}`];
  });
  return blocks.length > 0 ? blocks.join("\n") : `  (no failing ${label}s)\n`;
}
