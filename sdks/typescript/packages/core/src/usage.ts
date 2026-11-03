import type { Usage } from "./proto/types.js";

const INT_FIELDS = [
  "model_calls",
  "judge_calls",
  "input_tokens",
  "output_tokens",
  "cached_tokens",
  "unpriced_calls",
  "budget_refused",
] as const;
const USD_FIELDS = ["cost_usd", "retry_cost_usd"] as const;

/** Usage of an evaluation that made no model calls. */
export function zeroUsage(): Usage {
  return {
    model_calls: 0,
    judge_calls: 0,
    input_tokens: 0,
    output_tokens: 0,
    cached_tokens: 0,
    cost_usd: 0,
    retry_cost_usd: 0,
    unpriced_calls: 0,
    budget_refused: 0,
  };
}

/** Field-wise sum of two usages. */
export function addUsage(a: Usage, b: Usage): Usage {
  return {
    model_calls: a.model_calls + b.model_calls,
    judge_calls: a.judge_calls + b.judge_calls,
    input_tokens: a.input_tokens + b.input_tokens,
    output_tokens: a.output_tokens + b.output_tokens,
    cached_tokens: a.cached_tokens + b.cached_tokens,
    cost_usd: a.cost_usd + b.cost_usd,
    retry_cost_usd: a.retry_cost_usd + b.retry_cost_usd,
    unpriced_calls: a.unpriced_calls + b.unpriced_calls,
    budget_refused: a.budget_refused + b.budget_refused,
  };
}

/** Field-wise sum of any number of usages; zero for none. */
export function sumUsage(usages: Iterable<Usage>): Usage {
  let total = zeroUsage();
  for (const u of usages) total = addUsage(total, u);
  return total;
}

/**
 * Validate the `usage` object of an evaluate_batch result: all nine fields
 * present, non-negative, integers except the two `*_usd` amounts.
 */
export function parseUsage(raw: unknown): Usage {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    throw new TypeError(`evaluate_batch result: usage must be an object, got ${JSON.stringify(raw)}`);
  }
  const obj = raw as Record<string, unknown>;
  const field = (name: string, integer: boolean): number => {
    const v = obj[name];
    if (typeof v !== "number" || !Number.isFinite(v) || v < 0 || (integer && !Number.isInteger(v))) {
      const kind = integer ? "a non-negative integer" : "a non-negative number";
      throw new TypeError(`evaluate_batch result: usage.${name} must be ${kind}, got ${JSON.stringify(v)}`);
    }
    return v;
  };
  const ints = Object.fromEntries(INT_FIELDS.map((f) => [f, field(f, true)])) as Record<
    (typeof INT_FIELDS)[number],
    number
  >;
  const usd = Object.fromEntries(USD_FIELDS.map((f) => [f, field(f, false)])) as Record<
    (typeof USD_FIELDS)[number],
    number
  >;
  return { ...ints, ...usd };
}

/**
 * "{model_calls} model calls · {tokens} tokens ({cached} cached) · ${cost} · retry cost ${retry}",
 * plus refused and unpriced calls when present. Same format as the engine's
 * report.FormatUsage.
 */
export function formatUsage(u: Usage): string {
  let line =
    `${u.model_calls} model calls · ${u.input_tokens + u.output_tokens} tokens ` +
    `(${u.cached_tokens} cached) · $${u.cost_usd.toFixed(4)} · retry cost $${u.retry_cost_usd.toFixed(4)}`;
  if (u.budget_refused > 0) line += ` · ${u.budget_refused} calls refused by budget`;
  if (u.unpriced_calls > 0) line += ` · ${u.unpriced_calls} unpriced calls not in $`;
  return line;
}

/**
 * The run-summary line "attest usage: …" when the session made a model call
 * or refused one by budget; undefined otherwise.
 */
export function usageSummaryLine(u: Usage): string | undefined {
  if (u.model_calls === 0 && u.budget_refused === 0) return undefined;
  return `attest usage: ${formatUsage(u)}`;
}
