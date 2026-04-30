/**
 * Terminal-grade rendering of Attest assertion failures.
 *
 * Mirrors the Python attest.cli.diagnostics module so PR comments,
 * pytest output, and vitest output show the same fields. Use as the
 * message argument to a vitest `expect(result.passed, ...)` so
 * reviewers see trace path, expected vs actual, and judge metadata.
 */

import type { AssertionResult, JudgeMetadata } from "./proto/types.js";
import {
  STATUS_HARD_FAIL,
  STATUS_PASS,
  STATUS_SOFT_FAIL,
} from "./proto/constants.js";
import type { AgentResult } from "./result.js";

const ANSI_RESET = "[0m";
const ANSI_RED = "[31m";
const ANSI_YELLOW = "[33m";
const ANSI_GREEN = "[32m";
const ANSI_DIM = "[2m";
const ANSI_BOLD = "[1m";

const LAYER_NAMES: Record<number, string> = {
  1: "schema",
  2: "constraint",
  3: "trace",
  4: "content",
  5: "embedding",
  6: "llm_judge",
  7: "trace_tree",
  8: "plugin",
};

function layerLabel(layer: number, assertionType: string | undefined): string {
  if (layer === 0 && assertionType) {
    return `L? ${assertionType}`;
  }
  const name = LAYER_NAMES[layer] ?? assertionType ?? "uncategorized";
  return `L${layer} ${name}`;
}

function statusGlyph(status: string, color: boolean): string {
  let glyph: string;
  let c: string;
  if (status === STATUS_PASS) {
    glyph = "PASS";
    c = ANSI_GREEN;
  } else if (status === STATUS_SOFT_FAIL) {
    glyph = "SOFT";
    c = ANSI_YELLOW;
  } else if (status === STATUS_HARD_FAIL) {
    glyph = "FAIL";
    c = ANSI_RED;
  } else {
    glyph = status.toUpperCase() || "????";
    c = ANSI_DIM;
  }
  return color ? `${c}${glyph}${ANSI_RESET}` : glyph;
}

function bold(s: string, color: boolean): string {
  return color ? `${ANSI_BOLD}${s}${ANSI_RESET}` : s;
}

function dim(s: string, color: boolean): string {
  return color ? `${ANSI_DIM}${s}${ANSI_RESET}` : s;
}

function truncate(value: string, limit = 280): string {
  if (value.length <= limit) {
    return value;
  }
  return value.slice(0, limit - 3) + "...";
}

export interface RenderOptions {
  /** Emit ANSI colour escapes. Default false (plain text). */
  color?: boolean;
  /** Source location annotation appended after each block. */
  testFile?: string;
  /** Render passing assertions in addition to failures. Default false. */
  includePassing?: boolean;
}

export function renderAssertionFailure(
  result: AssertionResult,
  options: RenderOptions = {},
): string {
  const color = options.color ?? false;
  const testFile = options.testFile;
  const layer = result.layer ?? 0;
  const label = layerLabel(layer, result.type);

  const lines: string[] = [];
  lines.push(
    `  ${statusGlyph(result.status, color)} ${bold(result.assertion_id, color)} ${dim(`— ${label}`, color)}`,
  );
  if (result.trace_node_path) {
    lines.push(`      trace path: ${result.trace_node_path}`);
  }
  lines.push(`      score:      ${result.score.toFixed(3)}`);
  if (result.expected) {
    lines.push(`      expected:   ${truncate(result.expected)}`);
  }
  if (result.actual) {
    lines.push(`      actual:     ${truncate(result.actual)}`);
  }
  if (result.explanation) {
    lines.push(`      detail:     ${truncate(result.explanation)}`);
  }
  if (result.threshold_source && result.threshold_source !== "static") {
    lines.push(`      threshold:  ${result.threshold_source}`);
  }
  if (result.failure_class) {
    lines.push(`      class:      ${result.failure_class}`);
  }
  if (result.judge_metadata) {
    appendJudgeMetadata(lines, result.judge_metadata);
  }
  if (result.suggested_action) {
    lines.push(`      hint:       ${truncate(result.suggested_action)}`);
  }
  if ((result.cost ?? 0) > 0 || (result.duration_ms ?? 0) > 0) {
    const cost = result.cost ?? 0;
    const durMs = result.duration_ms ?? 0;
    lines.push(`      cost/lat:   $${cost.toFixed(6)} / ${durMs}ms`);
  }
  if (testFile) {
    lines.push(`      source:     ${testFile}`);
  }
  return lines.join("\n") + "\n";
}

function appendJudgeMetadata(lines: string[], meta: JudgeMetadata): void {
  if (meta.model) {
    let rubric = meta.rubric_name ?? "default";
    if (meta.rubric_version) {
      rubric = `${rubric} @ ${meta.rubric_version}`;
    }
    lines.push(`      judge:      ${meta.model} / ${rubric}`);
  }
  if (meta.prompt_hash) {
    lines.push(`      prompt:     #${meta.prompt_hash}`);
  }
  const samples = meta.sample_scores ?? [];
  if (samples.length > 1) {
    const formatted = samples.map((s) => s.toFixed(2)).join(", ");
    const mean = (meta.score_mean ?? 0).toFixed(2);
    const stddev = (meta.score_stddev ?? 0).toFixed(2);
    const flag = meta.high_variance ? " ⚠ HIGH VARIANCE" : "";
    lines.push(
      `      samples:    [${formatted}] mean=${mean} stddev=${stddev}${flag}`,
    );
  }
  const probes = meta.bias_probes ?? [];
  if (probes.length > 0) {
    const formatted = probes
      .map((p) => `${p.name} Δ${p.delta >= 0 ? "+" : ""}${p.delta.toFixed(2)}`)
      .join(", ");
    lines.push(`      bias:       ${formatted}`);
  }
  if (meta.calibration) {
    const c = meta.calibration;
    lines.push(
      `      calibrated: ${c.label_count} labels, agreement=${c.agreement.toFixed(2)}, κ=${c.cohen_kappa.toFixed(2)}`,
    );
  }
}

export function renderSummary(
  result: AgentResult,
  options: { color?: boolean } = {},
): string {
  const color = options.color ?? false;
  const passN = result.passCount;
  const soft = result.assertionResults.filter(
    (r) => r.status === STATUS_SOFT_FAIL,
  ).length;
  const hard = result.assertionResults.filter(
    (r) => r.status === STATUS_HARD_FAIL,
  ).length;
  const parts = [
    `${statusGlyph(STATUS_PASS, color)} ${passN}`,
    `${statusGlyph(STATUS_SOFT_FAIL, color)} ${soft}`,
    `${statusGlyph(STATUS_HARD_FAIL, color)} ${hard}`,
    `cost $${result.totalCost.toFixed(6)}`,
    `dur ${result.totalDurationMs}ms`,
  ];
  return parts.join(" | ");
}

export function renderDiagnostics(
  result: AgentResult,
  options: RenderOptions = {},
): string {
  const color = options.color ?? false;
  const includePassing = options.includePassing ?? false;
  const rows = includePassing
    ? result.assertionResults
    : result.failedAssertions;
  if (rows.length === 0) {
    return dim("  (no failures)\n", color);
  }
  const out: string[] = [""];
  out.push(
    bold(
      `Attest diagnostic — ${rows.length} of ${result.assertionResults.length} assertions failed:`,
      color,
    ),
  );
  for (const r of rows) {
    out.push(renderAssertionFailure(r, options));
  }
  out.push(dim(`  Summary: ${renderSummary(result, { color })}`, color));
  return out.join("\n");
}
