import { describe, expect, it } from "vitest";
import { zeroUsage } from "@attest-ai/core";
import { buildReport, type TestRecord } from "../src/report.js";

function record(name: string, budgetExceeded?: boolean): TestRecord {
  return {
    testId: `a.test.ts > ${name}`,
    name,
    file: "a.test.ts",
    line: 3,
    outcome: "passed",
    durationMs: 1,
    attest: {
      attempt: 0,
      results: [],
      totalCost: 0,
      totalDurationMs: 1,
      usage: zeroUsage(),
      ...(budgetExceeded === undefined ? {} : { budgetExceeded }),
    },
  };
}

describe("buildReport id_scheme and budget_exceeded", () => {
  it("writes id_scheme 2", () => {
    expect(buildReport([record("t")], false)["id_scheme"]).toBe(2);
  });

  it.each([false, true])("marks effect world exclusion for simulated=%s reports", (simulated) => {
    expect(buildReport([record("t")], simulated)["effect_world_excluded"]).toBe(true);
  });

  it("marks effect world exclusion for an empty report", () => {
    expect(buildReport([], false)["effect_world_excluded"]).toBe(true);
  });

  it("ORs budget_exceeded across the tests of the run", () => {
    expect(buildReport([record("a"), record("b", true), record("c")], false)["budget_exceeded"]).toBe(true);
  });

  it("is false when no batch of the run exceeded the budget", () => {
    expect(buildReport([record("a"), record("b")], false)["budget_exceeded"]).toBe(false);
  });

  it("carries a breach from an evaluation outside any test", () => {
    expect(buildReport([record("a")], false, zeroUsage(), false, true)["budget_exceeded"]).toBe(true);
  });
});
