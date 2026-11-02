/**
 * Capture: turn a run into a verified fixture, and fixtures into tests.
 *
 * `attest capture --name <name> -- <command>` (the engine) records a run's trace and model
 * exchanges into `tests/attest/fixtures/<name>.attest.json`. This module is the TypeScript
 * side:
 *
 * - `record()` captures the code in a callback in this process.
 * - `loadFixture()` reads a fixture (`proto/attest/v1/fixture-v1.schema.json`).
 * - `emitVitest()` writes the fixture's drafted contracts as a Vitest file, using the
 *   `attestExpect()` DSL. `attest capture` and `record()` call it.
 */

import { spawn } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { resolveEngine } from "./engine-manager.js";
import type { Trace } from "./proto/types.js";

export const FIXTURE_VERSION = 1;
export const DEFAULT_FIXTURE_DIR = path.join("tests", "attest", "fixtures");

/** A capture did not produce a fixture, or produced one that is not verified. */
export class CaptureError extends Error {
  /** The fixture written, when the engine wrote one. */
  readonly fixture: string | null;

  constructor(message: string, fixture: string | null = null) {
    super(message);
    this.name = "CaptureError";
    this.fixture = fixture;
  }
}

/** A drafted contract: an engine assertion spec plus its id and description. */
export interface Contract {
  id: string;
  kind: string;
  description: string;
  type: string;
  spec: Record<string, unknown>;
}

/** A contract's status on the captured trace. */
export interface ContractResult {
  contract: string;
  status: string;
  explanation: string;
}

/** A capture fixture (fixture-v1). */
export interface Fixture {
  name: string;
  source: { kind: string; replay_capable: boolean; command?: string[]; exit_code?: number };
  trace: Trace;
  exchanges: Record<string, unknown>[];
  contracts: Contract[];
  verification: {
    verified: boolean;
    expect_fail: boolean;
    trace_check: { ok: boolean; errors: number; warnings: number };
    results: ContractResult[];
    expected_failures: string[];
  };
  world: Record<string, unknown> | null;
}

function toPath(p: string | URL): string {
  return p instanceof URL ? fileURLToPath(p) : p;
}

/** Read a capture fixture written by `attest capture` or `record()`. */
export function loadFixture(file: string | URL): Fixture {
  const p = toPath(file);
  const doc = JSON.parse(fs.readFileSync(p, "utf8")) as Partial<Fixture> & { fixture_version?: unknown };
  if (doc === null || typeof doc !== "object" || doc.fixture_version !== FIXTURE_VERSION) {
    throw new CaptureError(`${p}: not a fixture_version ${FIXTURE_VERSION} capture fixture`);
  }
  return doc as Fixture;
}

// ---------------------------------------------------------------------------- emitter

/**
 * An object-literal key for `k`. `__proto__` must be a computed key: written plain or
 * quoted it sets the object's prototype instead of a property.
 */
function objectKey(k: string): string {
  if (k === "__proto__") return `[${JSON.stringify(k)}]`;
  return /^[A-Za-z_$][\w$]*$/.test(k) ? k : JSON.stringify(k);
}

/** Make captured text safe inside a `//` comment: one line, no control characters. */
function comment(text: string): string {
  return text.replace(/[\p{Cc}\p{Zl}\p{Zp}]/gu, " ");
}

/** Render a JSON value as a TypeScript literal. */
function ts(value: unknown): string {
  if (value === null || typeof value === "boolean" || typeof value === "number") {
    return JSON.stringify(value);
  }
  if (typeof value === "string") return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(ts).join(", ")}]`;
  if (typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>).map(
      ([k, v]) => `${objectKey(k)}: ${ts(v)}`,
    );
    return entries.length === 0 ? "{}" : `{ ${entries.join(", ")} }`;
  }
  throw new Error(`value ${String(value)} has no literal`);
}

function str(spec: Record<string, unknown>, key: string): string {
  const v = spec[key];
  if (typeof v !== "string") throw new Error(`missing ${key}`);
  return v;
}

function options(entries: Array<[string, unknown]>): string {
  const set = entries.filter(([, v]) => v !== undefined && v !== null && v !== false);
  return set.length === 0 ? "" : `{ ${set.map(([k, v]) => `${k}: ${ts(v)}`).join(", ")} }`;
}

function call(method: string, args: string[], opts: string): string {
  return `.${method}(${[...args, ...(opts ? [opts] : [])].join(", ")})`;
}

/** The `ExpectChain` method call that emits `contract`'s spec; throws when there is none. */
export function dslCall(contract: Contract): string {
  const s = contract.spec;
  const soft = s["soft"] === true ? true : undefined;
  try {
    if (contract.type === "trace") {
      const check = s["check"];
      if (check === "trajectory" && s["mode"] !== "partial_order") {
        const mode = s["mode"] === "strict" || s["mode"] === undefined ? undefined : s["mode"];
        if (!Array.isArray(s["tools"])) throw new Error("missing tools");
        return call("trajectory", [ts(s["tools"])], options([["mode", mode], ["soft", soft]]));
      }
      if (check === "tool_order") {
        return call("toolCalledBefore", [ts(str(s, "before")), ts(str(s, "after"))], options([["soft", soft]]));
      }
      if (check === "tool_cardinality") {
        return call(
          "toolCardinality",
          [ts(str(s, "tool"))],
          options([
            ["per", s["per"]],
            ["atLeast", s["min"]],
            ["atMost", s["max"]],
            ["soft", soft],
          ]),
        );
      }
    }
    if (contract.type === "schema" && s["target"] === "output.structured") {
      if (typeof s["schema"] !== "object" || s["schema"] === null) throw new Error("missing schema");
      return call("outputMatchesSchema", [ts(s["schema"])], "");
    }
    if (contract.type === "constraint" && s["field"] === "metadata.cost_usd" && s["operator"] === "lte") {
      if (typeof s["value"] !== "number") throw new Error("missing value");
      return call("costUnder", [ts(s["value"])], options([["soft", soft]]));
    }
  } catch (err) {
    throw new Error(`contract ${contract.id}: malformed spec (${(err as Error).message})`);
  }
  throw new Error(`contract ${contract.id}: no TypeScript DSL call for ${contract.type} spec`);
}

/**
 * Where the drafted test for a fixture goes: `tests/attest/<name>.test.ts` for
 * `tests/attest/fixtures/<name>.attest.json`.
 */
export function testPathFor(fixturePath: string): string {
  const name = path.basename(fixturePath).replace(/\.attest\.json$/, "");
  return path.join(path.dirname(path.dirname(fixturePath)), `${name}.test.ts`);
}

/**
 * Render a fixture's drafted contracts as a Vitest module.
 *
 * A verified fixture becomes a test that enforces every contract with `evaluate`. An
 * incident reproduction (`expect_fail`) becomes a test that checks, with `inspect`, that
 * the expected failures still fail and the other contracts still pass; it stays green
 * while it reproduces the incident.
 */
export function renderVitest(fixture: Fixture, fixturePath: string, testPath: string): string {
  const calls: Array<[Contract, string]> = [];
  const skipped: string[] = [];
  for (const c of fixture.contracts) {
    try {
      calls.push([c, dslCall(c)]);
    } catch (err) {
      skipped.push((err as Error).message);
    }
  }
  const rel = path.relative(path.dirname(testPath), fixturePath).split(path.sep).join("/");
  const status = new Map(fixture.verification.results.map((r) => [r.contract, r.status]));
  const expectFail = fixture.verification.expect_fail;

  const lines = [
    `// Contracts drafted by \`attest capture\` from fixture ${fixture.name}.`,
    "//",
    "// Review and edit before committing: they check the captured trace without running",
    "// the agent. Re-capturing does not overwrite this file.",
    `import { attestExpect, loadFixture, type ExpectChain } from "@attest-ai/core";`,
    `import { ${expectFail ? "inspect" : "evaluate"} } from "@attest-ai/vitest";`,
    `import { describe, ${expectFail ? "expect, " : ""}it } from "vitest";`,
    "",
    `const FIXTURE = loadFixture(new URL(${ts(rel.startsWith(".") ? rel : `./${rel}`)}, import.meta.url));`,
  ];
  if (skipped.length > 0) {
    lines.push("", "// Drafted contracts without a TypeScript DSL call (left out):");
    for (const s of skipped) lines.push(`// - ${comment(s)}`);
  }
  lines.push("", "function contracts(): ExpectChain {", "  return attestExpect(FIXTURE.trace)");
  for (const [c, dsl] of calls) {
    lines.push(`    // ${comment(`${c.id}: ${c.description}`)}`, `    ${dsl}`);
  }
  lines[lines.length - 1] += ";";
  lines.push("}");
  if (expectFail) {
    lines.push("", "// Status of each contract on the captured incident, in contracts() order.");
    lines.push("const EXPECTED: Array<[string, string]> = [");
    for (const [c] of calls) lines.push(`  [${ts(c.id)}, ${ts(status.get(c.id) ?? "pass")}],`);
    lines.push(
      "];",
      "",
      `describe(${ts(fixture.name)}, () => {`,
      `  it("reproduces the incident", async () => {`,
      "    const result = await inspect(contracts());",
      "",
      "    expect(result.assertionResults.map((row) => row.status)).toEqual(EXPECTED.map(([, s]) => s));",
      "  });",
      "});",
    );
  } else {
    lines.push(
      "",
      `describe(${ts(fixture.name)}, () => {`,
      `  it("keeps its drafted contracts", async () => {`,
      "    await evaluate(contracts());",
      "  });",
      "});",
    );
  }
  return `${lines.join("\n")}\n`;
}

/**
 * Write the drafted Vitest file for a fixture unless it already exists. Returns the path
 * written, or null when a test file is already there (re-capturing never overwrites an
 * edited test).
 */
export function emitVitest(fixturePath: string): string | null {
  const testPath = testPathFor(fixturePath);
  if (fs.existsSync(testPath)) return null;
  const source = renderVitest(loadFixture(fixturePath), fixturePath, testPath);
  fs.mkdirSync(path.dirname(testPath), { recursive: true });
  fs.writeFileSync(testPath, source, "utf8");
  return testPath;
}

// ---------------------------------------------------------------------------- record()

/** A running in-process capture. */
export interface Capture {
  name: string;
  /** The variables `record()` set in `process.env` for the callback. */
  env: Record<string, string>;
}

export interface RecordOptions {
  /** Fixture directory (default `tests/attest/fixtures`). */
  outDir?: string;
  /** Save an incident reproduction: verified when drafted contracts fail. */
  expectFail?: boolean;
  /** Trace `agent_id` (default: the first turn's agent name). */
  agentId?: string;
  /** Write the drafted Vitest file (default true). */
  emitTest?: boolean;
  /** Engine binary (default: the SDK's engine discovery). */
  enginePath?: string;
  /**
   * Called after the callback returns and before the fixture is built; pass the tracer
   * provider's `forceFlush` so batched spans reach the receiver.
   */
  flush?: () => unknown;
}

export interface RecordResult<T> {
  value: T;
  fixture: string;
  verified: boolean;
  testFile: string | null;
}

interface ServeExit {
  code: number | null;
  signal: NodeJS.Signals | null;
  stdout: string;
  stderr: string;
}

interface ServeProcess {
  ready: Promise<string | null>;
  finish(finalize: boolean): Promise<ServeExit>;
}

function serve(engine: string, args: string[]): ServeProcess {
  const child = spawn(engine, args, { stdio: ["pipe", "pipe", "pipe"] });
  let stdout = "";
  let stderr = "";
  let resolveReady: (line: string | null) => void = () => undefined;
  const ready = new Promise<string | null>((resolve) => {
    resolveReady = resolve;
  });
  const exited = new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((resolve) => {
    child.on("close", (code, signal) => {
      resolveReady(null);
      resolve({ code, signal });
    });
    child.on("error", (err) => {
      stderr += err.message;
      resolveReady(null);
      resolve({ code: null, signal: null });
    });
  });
  child.stdout.setEncoding("utf8").on("data", (chunk: string) => {
    stdout += chunk;
    const nl = stdout.indexOf("\n");
    if (nl >= 0) resolveReady(stdout.slice(0, nl));
  });
  child.stderr.setEncoding("utf8").on("data", (chunk: string) => {
    stderr += chunk;
  });
  // An engine that already exited cannot take "finalize"; its exit still says why.
  child.stdin.on("error", () => undefined);
  return {
    ready,
    async finish(finalize) {
      if (finalize) child.stdin.write("finalize\n");
      child.stdin.end();
      const { code, signal } = await exited;
      return { code, signal, stdout, stderr };
    },
  };
}

function readyEnv(line: string | null): Record<string, string> | null {
  if (line === null) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(line);
  } catch {
    return null;
  }
  const env = (parsed as { env?: unknown } | null)?.env;
  if (typeof env !== "object" || env === null) return null;
  const entries = Object.entries(env);
  if (!entries.every(([, v]) => typeof v === "string")) return null;
  return Object.fromEntries(entries) as Record<string, string>;
}

function exitDetail(exit: ServeExit): string {
  const how = exit.signal !== null ? `killed by ${exit.signal}` : `exit ${String(exit.code)}`;
  const err = exit.stderr.trim();
  return err ? `${how}: ${err}` : how;
}

function restore(saved: Map<string, string | undefined>): void {
  for (const [k, v] of saved) {
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  }
}

// record() sets process-wide environment variables, so a second concurrent capture
// would save and later restore the first one's values.
let recording = false;

/**
 * Capture `fn` as fixture `<outDir>/<name>.attest.json`.
 *
 * Starts `attest-engine capture --serve`, which hosts the recording proxy and the
 * OTLP/HTTP receiver on 127.0.0.1, and sets `OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL` and
 * `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` while `fn` runs. Model clients and the
 * OpenTelemetry exporter read these when they are constructed, so construct them inside
 * `fn`, and pass the tracer provider's `forceFlush` as `flush`. Afterwards the variables
 * are restored and the engine writes the fixture; for a verified fixture with `emitTest`
 * the drafted Vitest file is written unless one exists. If `fn` throws, the capture is
 * abandoned and no fixture is written. Captures cannot overlap.
 *
 * @throws CaptureError when another capture is running, the engine writes no fixture,
 *   or the fixture is not verified (its drafted contracts did not hold; the fixture is
 *   still written and no test is drafted).
 */
export async function record<T>(
  name: string,
  fn: (capture: Capture) => T | Promise<T>,
  opts: RecordOptions = {},
): Promise<RecordResult<T>> {
  if (recording) throw new CaptureError("record() is already running; captures cannot overlap");
  recording = true;
  try {
    return await recordExclusive(name, fn, opts);
  } finally {
    recording = false;
  }
}

async function recordExclusive<T>(
  name: string,
  fn: (capture: Capture) => T | Promise<T>,
  opts: RecordOptions,
): Promise<RecordResult<T>> {
  const engine = await resolveEngine(opts.enginePath);
  const args = ["capture", "--serve", "--name", name, "--out-dir", opts.outDir ?? DEFAULT_FIXTURE_DIR];
  if (opts.expectFail) args.push("--expect-fail");
  if (opts.agentId !== undefined) args.push("--agent-id", opts.agentId);
  const proc = serve(engine, args);
  const env = readyEnv(await proc.ready);
  if (env === null) {
    const exit = await proc.finish(false);
    throw new CaptureError(`attest-engine capture --serve did not start (${exitDetail(exit)})`);
  }
  const saved = new Map(Object.keys(env).map((k) => [k, process.env[k]]));
  Object.assign(process.env, env);
  let value: T;
  try {
    value = await fn({ name, env });
    if (opts.flush) await opts.flush();
  } catch (err) {
    restore(saved);
    await proc.finish(false);
    throw err;
  }
  restore(saved);
  const exit = await proc.finish(true);
  process.stderr.write(exit.stderr);
  const lines = exit.stdout.trim().split("\n");
  if (lines.length < 2) {
    throw new CaptureError(`capture ${name} wrote no fixture (${exitDetail(exit)})`);
  }
  const outcome = JSON.parse(lines[lines.length - 1] ?? "") as { fixture: string; verified: boolean };
  if (exit.code !== 0 || !outcome.verified) {
    throw new CaptureError(`capture ${name}: fixture ${outcome.fixture} is not verified; no test drafted`, outcome.fixture);
  }
  const testFile = opts.emitTest === false ? null : emitVitest(outcome.fixture);
  return { value, fixture: outcome.fixture, verified: true, testFile };
}
