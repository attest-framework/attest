/**
 * Behavioral conformance for adapters: run the shared corpus through the TypeScript adapters.
 *
 * The corpus (`sdks/parity/adapters/<adapter>/<case>.json`, written by `generate.py` from the
 * official SDK types) holds a provider response or framework run record and the golden
 * canonical trace. A case passes when the adapter's trace equals the golden exactly, except
 * `trace_id` (random per trace, replaced by `trc_normalized`). This mirrors
 * `sdks/python/tests/adapter_conformance.py`.
 */
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { vi } from "vitest";
import { mapStoredMessageToChatMessage } from "@langchain/core/messages";
import type { Trace } from "../../packages/core/src/proto/types.js";
import {
  AnthropicAdapter,
  GeminiAdapter,
  LangChainAdapter,
  ManualAdapter,
  OTelAdapter,
  OllamaAdapter,
  OpenAIAdapter,
} from "../../packages/core/src/adapters/index.js";
import { STEP_LLM_CALL, STEP_TOOL_CALL } from "../../packages/core/src/proto/constants.js";

const here = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(here, "../../../..");
export const CORPUS = join(REPO, "sdks/parity/adapters");
export const OTLP_GOLDEN = join(REPO, "protocol-tests/trace-v2/otlp");
export const ENGINE_PATH = join(REPO, "bin/attest-engine");
export const NORMALIZED_TRACE_ID = "trc_normalized";

type Json = null | boolean | number | string | Json[] | { [key: string]: Json };
type JsonObject = { [key: string]: Json };

export interface Case {
  adapter: string;
  case: string;
  description: string;
  sdk: { package: string; type: string };
  construct: JsonObject;
  call: JsonObject;
  payload: JsonObject;
  golden: JsonObject;
}

/**
 * Corpus directories without a TypeScript adapter, each with the reason. Listed
 * explicitly: a corpus directory that is neither run nor listed here fails the checker.
 */
export const PYTHON_ONLY: Readonly<Record<string, string>> = {
  crewai: "CrewAIAdapter has no TypeScript counterpart",
  "google-adk": "GoogleADKAdapter has no TypeScript counterpart",
  llamaindex: "LlamaIndexAdapter has no TypeScript counterpart",
};

/** Parity-matrix adapter names each corpus directory vouches for. */
export const MATRIX_NAMES: Readonly<Record<string, readonly string[]>> = {
  anthropic: ["AnthropicAdapter"],
  gemini: ["GeminiAdapter"],
  langchain: ["LangChainAdapter"],
  manual: ["ManualAdapter"],
  ollama: ["OllamaAdapter"],
  openai: ["OpenAIAdapter"],
  // OTLP -> trace conversion is the engine's; its goldens are protocol-tests/trace-v2/otlp.
  otel: ["OTelAdapter"],
};

export function loadCases(): Case[] {
  const cases: Case[] = [];
  for (const dir of readdirSync(CORPUS, { withFileTypes: true })) {
    if (!dir.isDirectory() || dir.name.startsWith(".")) continue;
    for (const file of readdirSync(join(CORPUS, dir.name)).sort()) {
      if (file.endsWith(".json")) {
        cases.push(JSON.parse(readFileSync(join(CORPUS, dir.name, file), "utf8")) as Case);
      }
    }
  }
  return cases;
}

export function caseId(c: Case): string {
  return `${c.adapter}/${c.case}`;
}

export function otlpCases(): string[] {
  return readdirSync(OTLP_GOLDEN)
    .filter((f) => f.endsWith(".otlp.json"))
    .sort()
    .map((f) => join(OTLP_GOLDEN, f));
}

/** A trace as plain JSON (what a golden holds): `undefined` fields dropped, `trace_id` normalized. */
export function canonical(trace: Trace): JsonObject {
  const data = JSON.parse(JSON.stringify(trace)) as JsonObject;
  data.trace_id = NORMALIZED_TRACE_ID;
  return data;
}

function kind(value: Json): string {
  if (value === null) return "null";
  return Array.isArray(value) ? "array" : typeof value;
}

/** Differences between two JSON values, one line per differing path. */
export function diff(expected: Json, actual: Json, path = "trace"): string[] {
  if (kind(expected) !== kind(actual)) {
    return [`${path}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`];
  }
  if (Array.isArray(expected) && Array.isArray(actual)) {
    const out: string[] = [];
    for (let i = 0; i < Math.max(expected.length, actual.length); i++) {
      if (i >= actual.length) out.push(`${path}[${i}]: missing, expected ${JSON.stringify(expected[i])}`);
      else if (i >= expected.length) out.push(`${path}[${i}]: unexpected ${JSON.stringify(actual[i])}`);
      else out.push(...diff(expected[i] as Json, actual[i] as Json, `${path}[${i}]`));
    }
    return out;
  }
  if (typeof expected === "object" && expected !== null && typeof actual === "object" && actual !== null) {
    const e = expected as JsonObject;
    const a = actual as JsonObject;
    const out: string[] = [];
    for (const key of [...new Set([...Object.keys(e), ...Object.keys(a)])].sort()) {
      if (!(key in a)) out.push(`${path}.${key}: missing, expected ${JSON.stringify(e[key])}`);
      else if (!(key in e)) out.push(`${path}.${key}: unexpected ${JSON.stringify(a[key])}`);
      else out.push(...diff(e[key] as Json, a[key] as Json, `${path}.${key}`));
    }
    return out;
  }
  return expected === actual ? [] : [`${path}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`];
}

function agentId(c: Case): string | undefined {
  return c.construct.agent_id as string | undefined;
}

function runProvider(c: Case): Trace {
  const call = c.call as {
    input_messages: Record<string, unknown>[];
    started_at_ms: number;
    ended_at_ms: number;
  };
  const options = {
    inputMessages: call.input_messages,
    startedAtMs: call.started_at_ms,
    endedAtMs: call.ended_at_ms,
  };
  const id = agentId(c);
  // Payloads are the serialized provider objects; each adapter takes its own wire shape.
  const payload = c.payload as never;
  switch (c.adapter) {
    case "openai":
      return new OpenAIAdapter(id).traceFromResponse(payload, options);
    case "anthropic":
      return new AnthropicAdapter(id).traceFromResponse(payload, options);
    case "gemini":
      return new GeminiAdapter(id).traceFromResponse(payload, options);
    case "ollama":
      return new OllamaAdapter(id).traceFromResponse(payload, options);
    default:
      throw new Error(`no provider adapter for corpus directory '${c.adapter}'`);
  }
}

const RUN_IDS = new Map<string, string>();

/** A stable UUID per recorded `run-N` name. */
function runUuid(name: string | null): string | undefined {
  if (name === null) return undefined;
  if (!RUN_IDS.has(name)) {
    RUN_IDS.set(name, `00000000-0000-4000-8000-${String(RUN_IDS.size + 1).padStart(12, "0")}`);
  }
  return RUN_IDS.get(name);
}

/** Decode the corpus's `$message` / `$llm_result` / `$error` wrappers to langchainjs objects. */
function decode(value: Json): unknown {
  if (Array.isArray(value)) return value.map(decode);
  if (typeof value !== "object" || value === null) return value;
  if ("$message" in value) {
    return mapStoredMessageToChatMessage(value.$message as never);
  }
  if ("$llm_result" in value) {
    const raw = value.$llm_result as {
      generations: { text: string; message: JsonObject }[][];
      llm_output: Json;
    };
    return {
      generations: raw.generations.map((row) =>
        row.map((g) => ({ text: g.text, message: decode({ $message: g.message }) })),
      ),
      llmOutput: raw.llm_output,
    };
  }
  if ("$error" in value) {
    return new Error((value.$error as { message: string }).message);
  }
  return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, decode(v)]));
}

interface CallbackEvent {
  t_ms: number;
  method: string;
  run_id: string;
  parent_run_id: string | null;
  args: JsonObject;
}

/** Replay recorded callbacks on the handler with the clock fixed at each event's `t_ms`. */
function replayLangChain(c: Case): Trace {
  RUN_IDS.clear();
  const handler = new LangChainAdapter(agentId(c));
  vi.useFakeTimers({ toFake: ["Date"] });
  try {
    for (const event of (c.payload as { events: CallbackEvent[] }).events) {
      vi.setSystemTime(event.t_ms);
      const args = decode(event.args) as Record<string, unknown>;
      const runId = runUuid(event.run_id) as string;
      const parent = runUuid(event.parent_run_id);
      switch (event.method) {
        case "on_chain_start":
          handler.handleChainStart({}, args.inputs as never, runId, parent);
          break;
        case "on_chain_end":
          handler.handleChainEnd(args.outputs as never, runId, parent);
          break;
        case "on_chat_model_start":
          handler.handleChatModelStart(
            {},
            args.messages as never,
            runId,
            parent,
            args.invocation_params as never,
          );
          break;
        case "on_llm_end":
          handler.handleLLMEnd(args.response as never, runId);
          break;
        case "on_tool_start": {
          // langchainjs takes the tool input as a string and names the run after the tool.
          const name = (args.serialized as { name: string }).name;
          handler.handleToolStart(
            {},
            JSON.stringify(args.inputs),
            runId,
            parent,
            undefined,
            undefined,
            name,
          );
          break;
        }
        case "on_tool_end":
          handler.handleToolEnd(args.output, runId);
          break;
        case "on_tool_error":
          handler.handleToolError(args.error, runId);
          break;
        default:
          throw new Error(`unhandled callback method '${event.method}'`);
      }
    }
  } finally {
    vi.useRealTimers();
  }
  return handler.buildTrace();
}

type BuilderOp = { op: string; args: Record<string, unknown> };

function runManual(c: Case): Trace {
  const ops = (c.payload as { ops: BuilderOp[] }).ops;
  return new ManualAdapter(agentId(c)).capture((builder) => {
    for (const { op, args } of ops) {
      switch (op) {
        case "set_input_dict":
          builder.setInput(args.input_data as Record<string, unknown>);
          break;
        case "set_output_dict":
          builder.setOutput(args.output_data as Record<string, unknown>);
          break;
        case "set_metadata":
          builder.setMetadata(args as never);
          break;
        case "add_llm_call":
        case "add_tool_call":
          // TraceBuilder.add*Call take no timestamps; a timed step goes in whole.
          builder.addStep({
            type: op === "add_llm_call" ? STEP_LLM_CALL : STEP_TOOL_CALL,
            name: args.name as string,
            args: args.args as Record<string, unknown>,
            result: args.result as Record<string, unknown>,
            started_at_ms: args.started_at_ms as number,
            ended_at_ms: args.ended_at_ms as number,
          });
          break;
        default:
          throw new Error(`unhandled builder op '${op}'`);
      }
    }
  });
}

/** Feed one corpus case through the TypeScript adapter. */
export function produce(c: Case): Trace {
  switch (c.call.kind) {
    case "trace_from_response":
      return runProvider(c);
    case "callbacks":
      return replayLangChain(c);
    case "builder_ops":
      return runManual(c);
    default:
      throw new Error(`no TypeScript runner for call kind '${String(c.call.kind)}' (${caseId(c)})`);
  }
}

/** Differences between the adapter's trace and the case's golden; empty on a pass. */
export function caseDiffs(c: Case): string[] {
  return diff(c.golden, canonical(produce(c)));
}

/** Differences between `OTelAdapter.fromOtlp` and the engine's golden for `path`. */
export async function otlpDiffs(path: string): Promise<string[]> {
  const golden = JSON.parse(readFileSync(path.replace(".otlp.", ".trace."), "utf8")) as Json;
  const trace = await OTelAdapter.fromOtlp(readFileSync(path, "utf8"));
  return diff(golden, JSON.parse(JSON.stringify(trace)) as Json);
}

/** `passing`: the adapter has cases and all pass; `failing`: some fail; `none`: no cases. */
export type ConformanceStatus = "passing" | "failing" | "none";

/** Conformance status per parity-matrix adapter name, running every case. */
export async function adapterStatuses(): Promise<Record<string, ConformanceStatus>> {
  const failing = new Set<string>();
  const covered = new Set<string>();
  for (const c of loadCases()) {
    if (c.adapter in PYTHON_ONLY) continue;
    for (const name of MATRIX_NAMES[c.adapter] ?? []) {
      covered.add(name);
      if (caseDiffs(c).length > 0) failing.add(name);
    }
  }
  for (const path of otlpCases()) {
    covered.add("OTelAdapter");
    if ((await otlpDiffs(path)).length > 0) failing.add("OTelAdapter");
  }
  const status: Record<string, ConformanceStatus> = {};
  for (const name of Object.values(MATRIX_NAMES).flat()) {
    status[name] = !covered.has(name) ? "none" : failing.has(name) ? "failing" : "passing";
  }
  return status;
}
