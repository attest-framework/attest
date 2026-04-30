import { describe, it, expect } from "vitest";
import {
  renderAssertionFailure,
  renderDiagnostics,
  renderSummary,
} from "../../packages/core/src/diagnostics.js";
import { AgentResult } from "../../packages/core/src/result.js";
import type {
  AssertionResult,
  Trace,
} from "../../packages/core/src/proto/types.js";
import {
  STATUS_HARD_FAIL,
  STATUS_PASS,
  STATUS_SOFT_FAIL,
  TYPE_CONTENT,
  TYPE_LLM_JUDGE,
} from "../../packages/core/src/proto/constants.js";

function makeAgentResult(
  ...assertions: AssertionResult[]
): AgentResult {
  const trace: Trace = {
    trace_id: "trc_test",
    output: { message: "hello" },
    steps: [],
  };
  const totalCost = assertions.reduce((acc, r) => acc + (r.cost ?? 0), 0);
  const totalDur = assertions.reduce(
    (acc, r) => acc + (r.duration_ms ?? 0),
    0,
  );
  return new AgentResult(trace, assertions, totalCost, totalDur);
}

describe("renderAssertionFailure", () => {
  it("includes every diagnostic field for a judge failure", () => {
    const result: AssertionResult = {
      assertion_id: "judge_001",
      status: STATUS_HARD_FAIL,
      score: 0.4,
      explanation: "weak rationale",
      cost: 0.012,
      duration_ms: 1820,
      layer: 6,
      type: TYPE_LLM_JUDGE,
      trace_node_path: "output.answer",
      expected: "judge_score >= 0.80",
      actual: "judge_score=0.40",
      suggested_action: "Calibrate judge or refine rubric.",
      threshold_source: "static",
      judge_metadata: {
        model: "gpt-4.1",
        rubric_name: "correctness",
        rubric_version: "v3",
        prompt_hash: "abcd1234",
        sample_scores: [0.4, 0.4, 0.6],
        score_mean: 0.466,
        score_stddev: 0.115,
        high_variance: false,
      },
    };
    const block = renderAssertionFailure(result);

    for (const needle of [
      "FAIL",
      "judge_001",
      "L6 llm_judge",
      "trace path: output.answer",
      "expected:   judge_score >= 0.80",
      "actual:     judge_score=0.40",
      "judge:      gpt-4.1 / correctness @ v3",
      "prompt:     #abcd1234",
      "samples:    [0.40, 0.40, 0.60] mean=0.47 stddev=0.12",
      "hint:       Calibrate judge",
      "cost/lat:",
    ]) {
      expect(block).toContain(needle);
    }
  });

  it("omits threshold source when static", () => {
    const result: AssertionResult = {
      assertion_id: "a",
      status: STATUS_PASS,
      score: 1.0,
      explanation: "ok",
      threshold_source: "static",
    };
    expect(renderAssertionFailure(result)).not.toContain("threshold:");
  });

  it("renders threshold source when dynamic_unavailable", () => {
    const result: AssertionResult = {
      assertion_id: "dyn",
      status: STATUS_SOFT_FAIL,
      score: 0.5,
      explanation: "dynamic unavailable",
      threshold_source: "dynamic_unavailable",
    };
    expect(renderAssertionFailure(result)).toContain(
      "threshold:  dynamic_unavailable",
    );
  });
});

describe("renderSummary", () => {
  it("emits one line with pass/soft/fail counts and cost", () => {
    const result = makeAgentResult(
      {
        assertion_id: "ok",
        status: STATUS_PASS,
        score: 1.0,
        explanation: "",
        cost: 0.01,
        duration_ms: 10,
      },
      {
        assertion_id: "bad",
        status: STATUS_HARD_FAIL,
        score: 0.0,
        explanation: "missing",
        type: TYPE_CONTENT,
        layer: 4,
        duration_ms: 2,
      },
    );
    const summary = renderSummary(result);
    expect(summary).toContain("PASS 1");
    expect(summary).toContain("FAIL 1");
    expect(summary).toContain("cost $0.01");
    expect(summary).toContain("dur 12ms");
  });
});

describe("renderDiagnostics", () => {
  it("shows only failing assertions by default", () => {
    const result = makeAgentResult(
      {
        assertion_id: "ok",
        status: STATUS_PASS,
        score: 1.0,
        explanation: "",
      },
      {
        assertion_id: "bad",
        status: STATUS_HARD_FAIL,
        score: 0.0,
        explanation: "missing",
        type: TYPE_CONTENT,
        layer: 4,
        trace_node_path: "output.message",
        expected: 'contains "thanks"',
        actual: "hello",
      },
    );
    const block = renderDiagnostics(result);
    expect(block).toContain("1 of 2 assertions failed");
    expect(block).toContain("bad");
    expect(block).not.toContain("ok");
  });

  it("includes passing assertions when requested", () => {
    const result = makeAgentResult({
      assertion_id: "ok",
      status: STATUS_PASS,
      score: 1.0,
      explanation: "",
    });
    const block = renderDiagnostics(result, { includePassing: true });
    expect(block).toContain("ok");
  });

  it("returns dim placeholder when no failures", () => {
    const result = makeAgentResult({
      assertion_id: "ok",
      status: STATUS_PASS,
      score: 1.0,
      explanation: "",
    });
    const block = renderDiagnostics(result);
    expect(block).toContain("(no failures)");
  });

  it("emits ANSI escapes when color is enabled", () => {
    const result = makeAgentResult({
      assertion_id: "bad",
      status: STATUS_HARD_FAIL,
      score: 0.0,
      explanation: "missing",
    });
    const block = renderDiagnostics(result, { color: true });
    expect(block).toMatch(/\x1b\[/);
  });
});
