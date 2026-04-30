import { describe, it, expect } from "vitest";
import { renderAssertionFailure } from "../../packages/core/src/diagnostics.js";
import {
  FailureClass,
  type AssertionResult,
} from "../../packages/core/src/proto/types.js";
import { STATUS_HARD_FAIL } from "../../packages/core/src/proto/constants.js";

describe("FailureClass", () => {
  it("constants match the engine wire-format strings", () => {
    expect(FailureClass.BrokenCode).toBe("broken_code");
    expect(FailureClass.FlakyJudge).toBe("flaky_judge");
    expect(FailureClass.BadRubric).toBe("bad_rubric");
    expect(FailureClass.MissingTraceData).toBe("missing_trace_data");
    expect(FailureClass.StochasticVariance).toBe("stochastic_variance");
  });

  it("renderAssertionFailure surfaces failure_class on its own line", () => {
    const r: AssertionResult = {
      assertion_id: "a1",
      status: STATUS_HARD_FAIL,
      score: 0.0,
      explanation: "schema mismatch",
      layer: 1,
      type: "schema",
      failure_class: FailureClass.BrokenCode,
    };
    const out = renderAssertionFailure(r, { color: false });
    expect(out).toContain("class:");
    expect(out).toContain("broken_code");
  });

  it("does not render a class line when failure_class is absent", () => {
    const r: AssertionResult = {
      assertion_id: "a1",
      status: STATUS_HARD_FAIL,
      score: 0.0,
      explanation: "x",
      layer: 1,
      type: "schema",
    };
    const out = renderAssertionFailure(r, { color: false });
    expect(out).not.toContain("class:");
  });
});
