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
 *   `attestExpect()` DSL. `attest capture` and `record()` call it. For a fixture captured
 *   from a command, the test re-runs that command under strict replay (`replayRun()`) and
 *   evaluates the contracts on the new run's trace.
 */

import { spawn } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
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
  const doc = JSON.parse(fs.readFileSync(p, "utf8")) as unknown;
  if (!isObject(doc) || doc["fixture_version"] !== FIXTURE_VERSION) {
    throw new CaptureError(`${p}: not a fixture_version ${FIXTURE_VERSION} capture fixture`);
  }
  validate(p, doc);
  return doc as unknown as Fixture;
}

/** Fixture names, as the engine accepts them (they become file names). */
export const NAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

type Kind = "string" | "boolean" | "object" | "array";

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function kindOf(v: unknown): Kind | "other" {
  if (Array.isArray(v)) return "array";
  if (isObject(v)) return "object";
  if (typeof v === "string" || typeof v === "boolean") return typeof v as Kind;
  return "other";
}

/** Check the fixture-v1 fields this module reads before using any of them. */
function validate(p: string, doc: Record<string, unknown>): void {
  const need = (where: string, obj: unknown, fields: Record<string, Kind>): Record<string, unknown> => {
    if (!isObject(obj)) throw new CaptureError(`${p}: ${where} is not an object`);
    for (const [key, kind] of Object.entries(fields)) {
      if (kindOf(obj[key]) !== kind) throw new CaptureError(`${p}: ${where}.${key} is missing or not a ${kind}`);
    }
    return obj;
  };
  need("fixture", doc, { name: "string", source: "object", trace: "object", exchanges: "array", contracts: "array", verification: "object" });
  if (!("world" in doc)) throw new CaptureError(`${p}: fixture.world is missing`);
  if (!NAME_PATTERN.test(doc["name"] as string)) {
    throw new CaptureError(`${p}: fixture name ${JSON.stringify(doc["name"])} is not a valid fixture name`);
  }
  const v = need("fixture.verification", doc["verification"], {
    verified: "boolean",
    expect_fail: "boolean",
    results: "array",
    expected_failures: "array",
  });
  (doc["contracts"] as unknown[]).forEach((c, i) =>
    need(`fixture.contracts[${i}]`, c, { id: "string", kind: "string", description: "string", type: "string", spec: "object" }),
  );
  (v["results"] as unknown[]).forEach((r, i) =>
    need(`fixture.verification.results[${i}]`, r, { contract: "string", status: "string", explanation: "string" }),
  );
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
 * Where a drafted replay test re-runs the fixture's source command: `cwd` is the directory
 * the capture ran in (the command's relative paths resolve there), `world` the world file
 * the capture hosted, if any.
 */
export interface Rerun {
  cwd: string;
  world?: string;
}

/**
 * Where the drafted test for a fixture goes: `tests/attest/<name>.test.ts` for
 * `tests/attest/fixtures/<name>.attest.json`.
 */
export function testPathFor(fixturePath: string): string {
  const name = path.basename(fixturePath).replace(/\.attest\.json$/, "");
  return path.join(path.dirname(path.dirname(fixturePath)), `${name}.test.ts`);
}

function relUrl(target: string, testPath: string): string {
  const rel = path.relative(path.dirname(testPath), target).split(path.sep).join("/");
  const url = rel === "" ? "." : rel;
  return `new URL(${ts(url.startsWith(".") ? url : `./${url}`)}, import.meta.url)`;
}

function chainLines(calls: Array<[Contract, string]>, subject: string, param: string): string[] {
  const lines = [`function contracts(${param}): ExpectChain {`, `  return attestExpect(${subject})`];
  for (const [c, dsl] of calls) {
    lines.push(`    // ${comment(`${c.id}: ${c.description}`)}`, `    ${dsl}`);
  }
  lines[lines.length - 1] += ";";
  lines.push("}");
  return lines;
}

function skippedLines(skipped: string[]): string[] {
  if (skipped.length === 0) return [];
  return ["", "// Drafted contracts without a TypeScript DSL call (left out):", ...skipped.map((s) => `// - ${comment(s)}`)];
}

function renderReplay(
  fixture: Fixture,
  calls: Array<[Contract, string]>,
  skipped: string[],
  fixturePath: string,
  testPath: string,
  rerun: Rerun,
): string {
  const meaning = fixture.verification.expect_fail
    ? [
        "// The recording is an incident: these contracts failed on it. The test passes once",
        "// the new run satisfies them, so it fails again if the agent regresses.",
      ]
    : ["// The test fails when the agent's behavior stops satisfying them."];
  const world = rerun.world === undefined ? "undefined" : relUrl(rerun.world, testPath);
  const lines = [
    `// Replay test drafted by \`attest capture\` from fixture ${comment(ts(fixture.name))}.`,
    "//",
    "// Re-runs the fixture's source command under strict replay with egress blocked (the",
    "// engine's `replay --mode strict --no-live`): the recorded model responses answer the",
    "// agent's calls, and a call that matches no recording fails the test. The drafted",
    "// contracts are evaluated on the NEW run's trace, not on the recording.",
    ...meaning,
    "//",
    "// A pass means the new run is consistent with the recording and satisfies the drafted",
    "// contracts; it does not prove the agent correct. Identities are compared by role",
    "// (relative identity): a run that swaps who plays which role misses, while the absolute",
    "// identity is not checked. Review and edit before committing. Re-capturing does not",
    "// overwrite this file.",
    `import { attestExpect, replayRun, type ExpectChain } from "@attest-ai/core";`,
    `import type { Trace } from "@attest-ai/core/proto";`,
    `import { evaluate } from "@attest-ai/vitest";`,
    `import { describe, it } from "vitest";`,
    "",
    `const FIXTURE = ${relUrl(fixturePath, testPath)};`,
    "// The directory `attest capture` ran in: the source command's relative paths start here.",
    `const RUN_DIR = ${relUrl(rerun.cwd, testPath)};`,
    `const WORLD = ${world};`,
    ...skippedLines(skipped),
    "",
    ...chainLines(calls, "trace", "trace: Trace"),
    "",
    `describe(${ts(fixture.name)}, () => {`,
    `  it("new run is consistent with the recording", async () => {`,
    "    const trace = await replayRun(FIXTURE, { cwd: RUN_DIR, world: WORLD });",
    "",
    "    await evaluate(contracts(trace));",
    "  });",
    "});",
  ];
  return `${lines.join("\n")}\n`;
}

/**
 * Render a fixture's drafted contracts as a Vitest module.
 *
 * With `rerun` and a fixture captured from a command, the test re-runs that command under
 * strict replay and evaluates every contract on the new run's trace (an incident
 * reproduction included: it passes once the agent no longer reproduces the incident).
 * Otherwise a verified fixture becomes a test that enforces every contract on the recorded
 * trace with `evaluate`, and an incident reproduction (`expect_fail`) a test that checks,
 * with `inspect`, that the expected failures still fail and the other contracts still
 * pass; it stays green while it reproduces the incident.
 */
export function renderVitest(
  fixture: Fixture,
  fixturePath: string,
  testPath: string,
  rerun?: Rerun,
): string {
  const calls: Array<[Contract, string]> = [];
  const skipped: string[] = [];
  for (const c of fixture.contracts) {
    try {
      calls.push([c, dslCall(c)]);
    } catch (err) {
      skipped.push((err as Error).message);
    }
  }
  if (rerun !== undefined && fixture.source.kind === "command" && fixture.source.replay_capable) {
    return renderReplay(fixture, calls, skipped, fixturePath, testPath, rerun);
  }
  const status = new Map(fixture.verification.results.map((r) => [r.contract, r.status]));
  const expectFail = fixture.verification.expect_fail;

  const lines = [
    `// Contracts drafted by \`attest capture\` from fixture ${comment(ts(fixture.name))}.`,
    "//",
    "// Review and edit before committing: they check the captured trace without running",
    "// the agent. Re-capturing does not overwrite this file.",
    `import { attestExpect, loadFixture, type ExpectChain } from "@attest-ai/core";`,
    `import { ${expectFail ? "inspect" : "evaluate"} } from "@attest-ai/vitest";`,
    `import { describe, ${expectFail ? "expect, " : ""}it } from "vitest";`,
    "",
    `const FIXTURE = loadFixture(${relUrl(fixturePath, testPath)});`,
    ...skippedLines(skipped),
    "",
    ...chainLines(calls, "FIXTURE.trace", ""),
  ];
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
 * edited test). `rerun` makes the test of a command fixture re-run the command under strict
 * replay (see `renderVitest`).
 */
export function emitVitest(fixturePath: string, rerun?: Rerun): string | null {
  const testPath = testPathFor(fixturePath);
  const existing = fs.lstatSync(testPath, { throwIfNoEntry: false });
  if (existing?.isSymbolicLink()) {
    throw new CaptureError(`${testPath} is a symbolic link; refusing to draft a test through it`);
  }
  if (existing !== undefined) return null;
  const source = renderVitest(loadFixture(fixturePath), fixturePath, testPath, rerun);
  fs.mkdirSync(path.dirname(testPath), { recursive: true });
  // Create exclusively: "wx" fails on any existing path, a link planted after
  // the check included, and never writes through it.
  try {
    fs.writeFileSync(testPath, source, { encoding: "utf8", flag: "wx" });
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "EEXIST") {
      throw new CaptureError(`${testPath} appeared while drafting; left unchanged`);
    }
    throw err;
  }
  return testPath;
}

// ------------------------------------------------------------------------ replayRun()

const OTHER_PROVIDER_KEYS = ["ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY"];
const REPLAY_TIMEOUT_MS = 300_000;
const STDERR_TAIL = 4000;

export interface ReplayRunOptions {
  /** Directory to run the command in (the one the capture ran in). */
  cwd?: string | URL;
  /** World file the capture hosted, when the fixture has a `world` section. */
  world?: string | URL;
  /** Engine binary; default is the SDK's discovery order. */
  enginePath?: string;
  /** Milliseconds before the run is abandoned (default 300000). */
  timeoutMs?: number;
}

/**
 * Re-run a fixture's source command under strict replay and return the new run's trace.
 *
 * Runs `attest-engine replay --mode strict --no-live --trace-out ... -- <command>`: the
 * command's model calls are answered from the fixture's recorded exchanges, a call that
 * matches no recording fails, and nothing reaches a provider (`--no-live`, the other
 * providers' keys removed, `OPENAI_API_KEY` a placeholder, `ATTEST_EGRESS=block` for any
 * attest guard inside the command).
 *
 * Rejects with `CaptureError` when the fixture has no replayable source command, a request
 * matched no recording, the command exited differently than when recorded, or no trace came
 * out.
 */
export async function replayRun(fixture: string | URL, options: ReplayRunOptions = {}): Promise<Trace> {
  const fp = path.resolve(toPath(fixture));
  const doc = loadFixture(fp);
  const command = doc.source.command;
  if (doc.source.kind !== "command" || !doc.source.replay_capable || !command || command.length === 0) {
    throw new CaptureError(`${fp}: not a command capture with recorded exchanges; cannot replay`);
  }
  if (doc.world !== null && options.world === undefined) {
    throw new CaptureError(`${fp}: captured with a world; pass the world file the capture ran with`);
  }
  const env: NodeJS.ProcessEnv = { ...process.env, OPENAI_API_KEY: "replay-needs-no-key", ATTEST_EGRESS: "block" };
  for (const key of OTHER_PROVIDER_KEYS) delete env[key];
  const engine = await resolveEngine(options.enginePath);
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "attest-replay-"));
  try {
    const traceFile = path.join(tmp, "trace.json");
    const argv = ["replay", "--fixture", fp, "--mode", "strict", "--no-live", "--trace-out", traceFile, "--json"];
    if (options.world !== undefined) argv.push("--world", path.resolve(toPath(options.world)));
    argv.push("--", ...command);
    const timeoutMs = options.timeoutMs ?? REPLAY_TIMEOUT_MS;
    const run = await runEngine(engine, argv, {
      cwd: options.cwd === undefined ? undefined : toPath(options.cwd),
      env,
      timeoutMs,
    });
    if (run.error !== null) throw new CaptureError(`${fp}: ${run.error}`);
    if (run.timedOut) throw new CaptureError(`${fp}: replay run exceeded ${timeoutMs}ms`);
    checkReplay(fp, doc, run);
    if (!fs.existsSync(traceFile)) {
      throw new CaptureError(`${fp}: the replay run exported no trace\n${tail(run.stderr)}`);
    }
    return JSON.parse(fs.readFileSync(traceFile, "utf8")) as Trace;
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

interface EngineRun {
  code: number | null;
  signal: NodeJS.Signals | null;
  stdout: string;
  stderr: string;
  timedOut: boolean;
  error: string | null;
}

function tail(stderr: string): string {
  return stderr.slice(-STDERR_TAIL);
}

/**
 * Fail unless every request was served and the command exited as it did when recorded. The
 * engine exits 1 for a miss and for a failing command alike, so the verdict comes from its
 * `--json` result. An incident recording exits non-zero; its replay must too.
 */
function checkReplay(fp: string, fixture: Fixture, run: EngineRun): void {
  let misses: number;
  let exitCode: number;
  try {
    const result = JSON.parse(run.stdout) as { exit_code: unknown; summary: { misses: unknown } };
    misses = Number(result.summary.misses);
    exitCode = Number(result.exit_code);
    if (!Number.isInteger(misses) || !Number.isInteger(exitCode)) throw new TypeError("not integers");
  } catch {
    throw new CaptureError(`${fp}: replay run exited ${run.code ?? run.signal}\n${tail(run.stderr)}`);
  }
  if (misses > 0) throw new CaptureError(`${fp}: ${misses} request(s) matched no recording\n${tail(run.stderr)}`);
  const recorded = fixture.source.exit_code ?? 0;
  if (exitCode !== recorded) {
    throw new CaptureError(`${fp}: the command exited ${exitCode}; the recording exited ${recorded}\n${tail(run.stderr)}`);
  }
}

function runEngine(
  engine: string,
  argv: string[],
  opts: { cwd: string | undefined; env: NodeJS.ProcessEnv; timeoutMs: number },
): Promise<EngineRun> {
  return new Promise((resolve) => {
    const child = spawn(engine, argv, { cwd: opts.cwd, env: opts.env, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    let timedOut = false;
    child.stdout.setEncoding("utf8").on("data", (chunk: string) => {
      stdout += chunk;
    });
    child.stderr.setEncoding("utf8").on("data", (chunk: string) => {
      stderr += chunk;
    });
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGKILL");
    }, opts.timeoutMs);
    child.on("error", (err) => {
      clearTimeout(timer);
      resolve({ code: null, signal: null, stdout, stderr, timedOut, error: `could not start the replay run: ${err.message}` });
    });
    child.on("close", (code, signal) => {
      clearTimeout(timer);
      resolve({ code, signal, stdout, stderr, timedOut, error: null });
    });
  });
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
   * A `world.yaml` to host for the callback (the engine's `--world`): its base URL variable
   * (`PAYMENTS_BASE_URL`) is set for the callback and the world's final state is saved in
   * the fixture's `world` section.
   */
  world?: string;
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
  /**
   * `verified`, `not_verified` or `nothing_to_verify`: a healthy run with no tool call,
   * structured output or cost to draft a contract from. `record` throws for
   * `not_verified`; `nothing_to_verify` returns `verified: false` and no test file.
   */
  outcome: "verified" | "nothing_to_verify";
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
      // The environment as it is now: secrets the callback loaded (a dotenv
      // file, for example) are redacted too.
      if (finalize) {
        const environ = Object.entries(process.env).flatMap(([k, v]) => (v === undefined ? [] : [`${k}=${v}`]));
        child.stdin.write(`${JSON.stringify({ finalize: true, environ })}\n`);
      }
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
 *   or the fixture is not verified (a drafted contract did not hold, the trace check
 *   failed or no model call was recorded; the fixture is still written and no test is
 *   drafted). A healthy run with nothing to draft a contract from does not throw: the
 *   result has `outcome: "nothing_to_verify"`, `verified: false` and no test file.
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
  if (opts.world !== undefined) args.push("--world", path.resolve(opts.world));
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
  const outcome = JSON.parse(lines[lines.length - 1] ?? "") as {
    fixture: string;
    verified: boolean;
    outcome: "verified" | "not_verified" | "nothing_to_verify";
  };
  if (exit.code !== 0 || outcome.outcome === "not_verified") {
    throw new CaptureError(`capture ${name}: fixture ${outcome.fixture} is not verified; no test drafted`, outcome.fixture);
  }
  if (outcome.outcome === "nothing_to_verify") {
    return { value, fixture: outcome.fixture, verified: false, outcome: "nothing_to_verify", testFile: null };
  }
  const testFile = opts.emitTest === false ? null : emitVitest(outcome.fixture);
  return { value, fixture: outcome.fixture, verified: true, outcome: "verified", testFile };
}
