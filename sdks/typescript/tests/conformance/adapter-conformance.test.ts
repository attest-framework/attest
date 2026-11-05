import { describe, expect, it } from "vitest";
import { existsSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { AnthropicAdapter, OpenAIAdapter } from "../../packages/core/src/adapters/index.js";
import {
  CORPUS,
  ENGINE_PATH,
  MATRIX_NAMES,
  PYTHON_ONLY,
  canonical,
  caseDiffs,
  caseId,
  diff,
  loadCases,
  otlpCases,
  otlpDiffs,
  produce,
  type Case,
} from "../support/adapter-conformance.js";

const CASES = loadCases();

function byId(id: string): Case {
  const found = CASES.find((c) => caseId(c) === id);
  if (found === undefined) throw new Error(`no corpus case ${id}`);
  return found;
}

describe("adapter corpus", () => {
  for (const c of CASES) {
    const run = c.adapter in PYTHON_ONLY ? it.skip : it;
    run(`${caseId(c)} produces its golden trace`, () => {
      expect(caseDiffs(c)).toEqual([]);
    });
  }

  it("lists the Python-only adapters explicitly and runs every other corpus directory", () => {
    const directories = readdirSync(CORPUS, { withFileTypes: true })
      .filter((d) => d.isDirectory() && !d.name.startsWith("."))
      .map((d) => d.name);

    const unaccounted = directories.filter((d) => !(d in MATRIX_NAMES) && !(d in PYTHON_ONLY));
    const both = Object.keys(PYTHON_ONLY).filter((d) => d in MATRIX_NAMES);

    expect({ unaccounted, both }).toEqual({ unaccounted: [], both: [] });
  });

  it("runs the engine's OTLP goldens through OTelAdapter.fromOtlp", async () => {
    expect(existsSync(ENGINE_PATH), `build the engine first: make engine (${ENGINE_PATH})`).toBe(true);
    process.env.ATTEST_ENGINE_PATH = ENGINE_PATH;
    const files = otlpCases();

    const problems = (await Promise.all(files.map(otlpDiffs))).flat();

    expect(files.length).toBeGreaterThan(0);
    expect(problems).toEqual([]);
  });
});

describe("conformance checker", () => {
  it("reports a planted adapter divergence", () => {
    const c = byId("openai/chat_tool_call");
    const clone = structuredClone(c.payload) as { choices: { message: Record<string, unknown> }[] };
    clone.choices[0].message.tool_calls = [];

    const trace = new OpenAIAdapter().traceFromResponse(clone as never, {
      inputMessages: (c.call as { input_messages: Record<string, unknown>[] }).input_messages,
      startedAtMs: 1000,
      endedAtMs: 1250,
    });
    const problems = diff(c.golden, canonical(trace));

    expect(problems).toEqual([
      `trace.steps[1]: missing, expected ${JSON.stringify((c.golden.steps as unknown[])[1])}`,
    ]);
  });

  it("reports a planted tool-order divergence", () => {
    const c = byId("anthropic/tool_use");
    const trace = produce(c);
    const swapped = { ...trace, steps: [...trace.steps].reverse() };

    const problems = diff(c.golden, canonical(swapped));

    expect(problems.length).toBeGreaterThan(0);
    expect(problems[0]).toContain("trace.steps[0]");
  });

  it("distinguishes value types", () => {
    expect(diff({ tokens: 1 }, { tokens: "1" })).toEqual(['trace.tokens: expected 1, got "1"']);
  });

  it("replaces only the trace id", () => {
    const c = byId("openai/chat_text");

    const trace = produce(c);

    expect(trace.trace_id).not.toBe("trc_normalized");
    expect(canonical(trace)).toEqual(c.golden);
  });

  it("reads Anthropic server_tool_use blocks as tool calls", () => {
    const c = byId("anthropic/server_tool_use");

    const trace = new AnthropicAdapter().traceFromResponse(c.payload as never);

    expect(trace.steps.map((s) => s.name)).toEqual(["completion", "web_search"]);
  });

  it("finds every corpus case file", () => {
    expect(existsSync(join(CORPUS, "versions.json"))).toBe(true);
    expect(CASES.length).toBeGreaterThan(0);
  });
});
