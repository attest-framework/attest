/**
 * `ATTEST_REPORT_TRACES=1`: the report embeds the evaluated traces (`traces`, row `trace_id`),
 * validated against proto/attest/v1/report-v2.schema.json.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { AgentResult, zeroUsage, type Trace } from "@attest-ai/core";
import { buildReport, reportTracesEnabled, type TestRecord } from "../src/report.js";
import { recordOnTest } from "../src/test-state.js";
import { reportV2Validator } from "./report-schema.js";

const trace = (id: string, message: string): Trace => ({
  trace_id: id,
  output: { message },
  steps: [],
});

const rowOf = (id: string) => ({ assertion_id: id, status: "pass", score: 1, explanation: "ok" });

function record(name: string, results: TestRecord["attest"] extends infer A ? A : never): TestRecord {
  return { testId: `a.test.ts > ${name}`, name, file: "a.test.ts", line: 3, outcome: "passed", durationMs: 1, attest: results };
}

function meta(rows: { assertion_id: string; trace_id?: string }[], traces?: Record<string, Trace>) {
  return {
    attempt: 0,
    results: rows.map((r) => ({ ...rowOf(r.assertion_id), ...(r.trace_id === undefined ? {} : { trace_id: r.trace_id }) })),
    totalCost: 0,
    totalDurationMs: 1,
    usage: zeroUsage(),
    ...(traces === undefined ? {} : { traces }),
  };
}

afterEach(() => vi.unstubAllEnvs());

describe("buildReport traces", () => {
  it("omits traces and trace_id when not requested", () => {
    const report = buildReport([record("t", meta([{ assertion_id: "a" }]))], false) as Record<string, unknown>;

    expect(report["traces"]).toBeUndefined();
    expect((report["results"] as Record<string, unknown>[])[0]?.["trace_id"]).toBeUndefined();
  });

  it("re-keys per-test traces into one report-wide map that matches the schema", () => {
    const first = record("one", meta([{ assertion_id: "a", trace_id: "trc" }], { trc: trace("trc", "x") }));
    const second = record("two", meta([{ assertion_id: "b", trace_id: "trc" }], { trc: trace("trc", "y") }));
    const same = record("three", meta([{ assertion_id: "c", trace_id: "trc" }], { trc: trace("trc", "x") }));

    const report = buildReport([first, second, same], false, zeroUsage(), true) as {
      results: { trace_id: string }[];
      traces: Record<string, Trace>;
    };

    expect(report.results.map((r) => r.trace_id)).toEqual(["trc", "trc#2", "trc"]);
    expect(Object.keys(report.traces)).toEqual(["trc", "trc#2"]);
    expect(report.traces["trc#2"]?.output).toEqual({ message: "y" });
    const validate = reportV2Validator();
    expect(validate(report), JSON.stringify(validate.errors)).toBe(true);
  });
});

describe("recordOnTest traces", () => {
  const running = () => ({ meta: {}, result: { retryCount: 0 } }) as unknown as Parameters<typeof recordOnTest>[0];
  const result = new AgentResult(trace("trc_1", "ok"), [{ assertion_id: "a", status: "pass", score: 1, explanation: "ok" }]);

  it("tags rows and stores the trace under ATTEST_REPORT_TRACES=1", () => {
    vi.stubEnv("ATTEST_REPORT_TRACES", "1");
    const test = running();

    recordOnTest(test, result, undefined);

    expect(reportTracesEnabled()).toBe(true);
    expect(test.meta.attest?.results[0]?.trace_id).toBe("trc_1");
    expect(test.meta.attest?.traces).toEqual({ trc_1: result.trace });
  });

  it.each(["", "0", "true"])("records no trace when the variable is %j", (value) => {
    vi.stubEnv("ATTEST_REPORT_TRACES", value);
    const test = running();

    recordOnTest(test, result, undefined);

    expect(test.meta.attest?.traces).toBeUndefined();
    expect(test.meta.attest?.results[0]?.trace_id).toBeUndefined();
  });
});
