/**
 * OTelAdapter: OTLP/JSON encoding and the engine call contract. The OTLP → trace mapping
 * lives in the engine and is tested there (engine/internal/trace);
 * tests/conformance/trace-v2-conformance.test.ts runs the real engine.
 */
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { OTelAdapter, spansToOtlp, type OTelSpan } from "../../../packages/core/src/adapters/otel.js";
import { OTLPConversionError } from "../../../packages/core/src/index.js";
import { ENGINE_VERSION } from "../../../packages/core/src/version.js";

const TRACE_ID = "4bf92f3577b34da6a3ce929d0e0e4736";

function span(overrides: Partial<OTelSpan> & { name: string; spanId: string }): OTelSpan {
  return {
    startTime: [1, 500],
    endTime: [2, 0],
    attributes: {},
    ...overrides,
    spanContext: () => ({ traceId: TRACE_ID, spanId: overrides.spanId }),
  };
}

function encodedSpans(doc: Record<string, unknown>): Record<string, unknown>[] {
  const resource = (doc["resourceSpans"] as Record<string, unknown>[])[0]!;
  const scope = (resource["scopeSpans"] as Record<string, unknown>[])[0]!;
  return scope["spans"] as Record<string, unknown>[];
}

describe("spansToOtlp()", () => {
  it("encodes ids, nanosecond times, parent, status and attribute values", () => {
    const doc = spansToOtlp([
      span({ name: "invoke_agent a", spanId: "0000000000000001", attributes: { "gen_ai.agent.name": "a" } }),
      span({
        name: "chat m",
        spanId: "0000000000000002",
        parentSpanContext: { spanId: "0000000000000001" },
        status: { code: 2, message: "rate limited" },
        attributes: {
          int: 3,
          double: 0.5,
          flag: true,
          reasons: ["stop", 1],
          dropped: undefined,
        },
      }),
    ]);

    const [root, child] = encodedSpans(doc);
    expect(root).toEqual({
      traceId: TRACE_ID,
      spanId: "0000000000000001",
      name: "invoke_agent a",
      startTimeUnixNano: "1000000500",
      endTimeUnixNano: "2000000000",
      attributes: [{ key: "gen_ai.agent.name", value: { stringValue: "a" } }],
    });
    expect(child!["parentSpanId"]).toBe("0000000000000001");
    expect(child!["status"]).toEqual({ code: 2, message: "rate limited" });
    expect(child!["attributes"]).toEqual([
      { key: "int", value: { intValue: "3" } },
      { key: "double", value: { doubleValue: 0.5 } },
      { key: "flag", value: { boolValue: true } },
      {
        key: "reasons",
        value: { arrayValue: { values: [{ stringValue: "stop" }, { intValue: "1" }] } },
      },
    ]);
  });

  it("reads the 1.x parentSpanId field", () => {
    const [encoded] = encodedSpans(
      spansToOtlp([span({ name: "x", spanId: "0000000000000002", parentSpanId: "0000000000000001" })]),
    );
    expect(encoded!["parentSpanId"]).toBe("0000000000000001");
  });

  it("sends whole numbers beyond the safe integer range as doubles", () => {
    const [encoded] = encodedSpans(
      spansToOtlp([span({ name: "x", spanId: "0000000000000001", attributes: { big: 1e21 } })]),
    );
    expect(encoded!["attributes"]).toEqual([{ key: "big", value: { doubleValue: 1e21 } }]);
  });

  it("rejects a fractional HrTime naming the span", () => {
    expect(() =>
      spansToOtlp([span({ name: "chat", spanId: "0000000000000001", startTime: [1.5, 0] })]),
    ).toThrow(/"chat"/);
  });

  it("rejects non-finite numbers", () => {
    expect(() =>
      spansToOtlp([span({ name: "x", spanId: "0000000000000001", attributes: { cost: Number.NaN } })]),
    ).toThrow(RangeError);
  });
});

describe("OTelAdapter engine call", () => {
  let dir: string;
  const savedPath = process.env["ATTEST_ENGINE_PATH"];

  function scriptEngine(response: string, exitCode = 0, stderr = ""): void {
    fs.writeFileSync(path.join(dir, "response"), response);
    const engine = path.join(dir, "attest-engine");
    fs.writeFileSync(
      engine,
      [
        "#!/bin/sh",
        'case "$1" in',
        `  version) echo "attest-engine ${ENGINE_VERSION}" ;;`,
        `  trace) printf '%s\\n' "$@" > "${dir}/args"; cat > "${dir}/stdin"; cat "${dir}/response"`,
        `         printf '%s' "${stderr}" >&2; exit ${exitCode} ;;`,
        "esac",
      ].join("\n"),
    );
    fs.chmodSync(engine, 0o755);
    process.env["ATTEST_ENGINE_PATH"] = engine;
  }

  beforeEach(() => {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), "attest-otel-"));
  });

  afterEach(() => {
    if (savedPath === undefined) delete process.env["ATTEST_ENGINE_PATH"];
    else process.env["ATTEST_ENGINE_PATH"] = savedPath;
    fs.rmSync(dir, { recursive: true, force: true });
  });

  it("sends the export to `trace convert -` and returns the engine's trace", async () => {
    const trace = {
      schema_version: 2,
      trace_id: "otel_4bf92f3577b34da6",
      agent_id: "custom",
      input: {},
      output: { message: "ok" },
      steps: [{ type: "tool_call", name: "t", args: {}, result: {}, tool_call_id: "c1" }],
    };
    scriptEngine(JSON.stringify(trace));
    const otlp = { resourceSpans: [] };

    const result = await OTelAdapter.fromOtlp(otlp, "custom");

    expect(fs.readFileSync(path.join(dir, "args"), "utf8").trim().split("\n")).toEqual([
      "trace",
      "convert",
      "--agent-id",
      "custom",
      "-",
    ]);
    expect(JSON.parse(fs.readFileSync(path.join(dir, "stdin"), "utf8"))).toEqual(otlp);
    expect(result).toEqual(trace);
  });

  it("fromSpans encodes spans before converting", async () => {
    scriptEngine(JSON.stringify({ schema_version: 2, trace_id: "t", output: { message: "" }, steps: [] }));

    await OTelAdapter.fromSpans([span({ name: "chat", spanId: "0000000000000001" })]);

    const sent = JSON.parse(fs.readFileSync(path.join(dir, "stdin"), "utf8")) as Record<string, unknown>;
    expect(encodedSpans(sent)[0]!["name"]).toBe("chat");
  });

  it("rejects a parsed export holding a non-finite number", async () => {
    await expect(OTelAdapter.fromOtlp({ x: Number.NaN })).rejects.toThrow(RangeError);
  });

  it("raises OTLPConversionError when the engine prints no trace", async () => {
    scriptEngine("not json");

    await expect(OTelAdapter.fromOtlp("{}")).rejects.toThrow(/not JSON/);
  });

  it("raises OTLPConversionError with the engine's message on rejection", async () => {
    scriptEngine("", 1, "trace convert: traceId is not hex");

    await expect(OTelAdapter.fromOtlp("{}")).rejects.toThrow(OTLPConversionError);
    await expect(OTelAdapter.fromOtlp("{}")).rejects.toThrow("traceId is not hex");
  });
});
