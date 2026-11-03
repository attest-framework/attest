/**
 * report-v2 rows of a trials test (`trial`, `trials`) and `summary.usage`,
 * validated against proto/attest/v1/report-v2.schema.json, and the run-summary lines.
 */
import { describe, expect, it } from "vitest";
import { zeroUsage, type Usage } from "@attest-ai/core";
import { buildReport, summaryLines, type TestRecord, type TrialsMeta } from "../src/report.js";
import { reportV2Validator } from "./report-schema.js";

const usage = (u: Partial<Usage>): Usage => ({ ...zeroUsage(), ...u });

function record(name: string, attest: TestRecord["attest"]): TestRecord {
  return {
    testId: `tests/agent.test.ts > ${name}`,
    name,
    file: "tests/agent.test.ts",
    line: 7,
    outcome: "passed",
    durationMs: 12.4,
    attest,
  };
}

const trialsMeta: TrialsMeta = {
  requested: 3,
  run: 3,
  passed: 2,
  evaluated: 3,
  notEvaluated: 0,
  stoppedByBudget: false,
};

const trialsTest = record("refunds once", {
  attempt: 0,
  results: [
    { assertion_id: "assert_aaaaaaaaaaaa", status: "pass", score: 1, explanation: "ok", trial: 1 },
    {
      assertion_id: "assert_aaaaaaaaaaaa",
      status: "hard_fail",
      score: 0,
      explanation: "trial 2 raised Error: HTTP 500",
      failure_class: "broken_code",
      trial: 2,
    },
    { assertion_id: "assert_aaaaaaaaaaaa", status: "pass", score: 1, explanation: "ok", trial: 3 },
  ],
  totalCost: 0.03,
  totalDurationMs: 30,
  usage: usage({ model_calls: 3, judge_calls: 3, input_tokens: 900, output_tokens: 90, cached_tokens: 100, cost_usd: 0.03 }),
  trials: trialsMeta,
});

const singleTest = record("greets", {
  attempt: 0,
  results: [{ assertion_id: "assert_bbbbbbbbbbbb", status: "pass", score: 1, explanation: "ok" }],
  totalCost: 0.01,
  totalDurationMs: 5,
  usage: usage({ model_calls: 1, judge_calls: 1, input_tokens: 100, output_tokens: 10, cost_usd: 0.01, retry_cost_usd: 0.002 }),
});

describe("report-v2 for trials", () => {
  const report = buildReport([trialsTest, singleTest, record("no attest", undefined)], false) as {
    summary: Record<string, unknown>;
    results: Record<string, unknown>[];
    tests: Record<string, unknown>[];
  };

  it("matches the report-v2 schema", () => {
    const validate = reportV2Validator();
    expect(validate(report), JSON.stringify(validate.errors)).toBe(true);
  });

  it("tags trial rows with their trial and the trials test row with the count run", () => {
    expect(report.results.map((r) => r["trial"])).toEqual([1, 2, 3, undefined]);
    expect(report.tests[0]).toMatchObject({ trials: 3, assertion_ids: ["assert_aaaaaaaaaaaa"] });
    expect(report.tests[1]).not.toHaveProperty("trials");
  });

  it("sums the session usage into summary.usage", () => {
    expect(report.summary["usage"]).toEqual({
      model_calls: 4,
      judge_calls: 4,
      input_tokens: 1000,
      output_tokens: 100,
      cached_tokens: 100,
      cost_usd: 0.04,
      retry_cost_usd: 0.002,
      unpriced_calls: 0,
      budget_refused: 0,
    });
  });

  it("writes an all-zero summary.usage when no model was called", () => {
    const empty = buildReport([record("no attest", undefined)], false) as { summary: Record<string, unknown> };
    expect(empty.summary["usage"]).toEqual(zeroUsage());
    const validate = reportV2Validator();
    expect(validate(empty), JSON.stringify(validate.errors)).toBe(true);
  });
});

describe("summaryLines", () => {
  it("prints one line per trials test and the usage line", () => {
    expect(summaryLines([trialsTest, singleTest])).toEqual([
      "tests/agent.test.ts > refunds once: 2/3 trials passed (0 not evaluated)",
      "attest usage: 4 model calls · 1100 tokens (100 cached) · $0.0400 · retry cost $0.0020",
    ]);
  });

  it("notes a budget stop and appends refused and unpriced calls", () => {
    const stopped = record("stopped", {
      attempt: 0,
      results: [],
      totalCost: 0,
      totalDurationMs: 0,
      usage: usage({ model_calls: 2, budget_refused: 1, unpriced_calls: 2 }),
      trials: { ...trialsMeta, requested: 5, run: 2, passed: 1, evaluated: 1, notEvaluated: 1, stoppedByBudget: true },
    });
    expect(summaryLines([stopped])).toEqual([
      "tests/agent.test.ts > stopped: 1/1 trials passed (1 not evaluated)",
      "tests/agent.test.ts > stopped: stopped after 2 of 5 trials: test budget spent",
      "attest usage: 2 model calls · 0 tokens (0 cached) · $0.0000 · retry cost $0.0000 · 1 calls refused by budget · 2 unpriced calls not in $",
    ]);
  });

  it("prints the usage line when calls were only refused, and nothing without model calls", () => {
    const refusedOnly = record("refused", {
      attempt: 0,
      results: [],
      totalCost: 0,
      totalDurationMs: 0,
      usage: usage({ budget_refused: 3 }),
    });
    expect(summaryLines([refusedOnly])).toEqual([
      "attest usage: 0 model calls · 0 tokens (0 cached) · $0.0000 · retry cost $0.0000 · 3 calls refused by budget",
    ]);
    expect(summaryLines([record("none", undefined)])).toEqual([]);
  });
});
