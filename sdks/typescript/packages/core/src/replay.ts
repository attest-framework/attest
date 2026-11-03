/**
 * Replay: run the agent's real code with model responses served from a fixture.
 *
 * `startReplay(name)` starts `attest-engine replay --serve`, which hosts the replay
 * proxy on 127.0.0.1, and sets `OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL` and
 * `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` until `finish()`. The proxy answers every model
 * request from `<fixturesDir>/<name>.attest.json` and makes no provider call. A request
 * that matches no recording is a miss; `finish()` returns the engine's summary.
 */

import { spawn } from "node:child_process";
import * as path from "node:path";
import { DEFAULT_FIXTURE_DIR, NAME_PATTERN } from "./capture.js";
import { resolveEngine } from "./engine-manager.js";

export type ReplayMode = "strict" | "auto";
export type ReplayModeSetting = ReplayMode | "off";

/** One request the agent made during the replay. */
export interface ReplayRecord {
  seq: number;
  provider: string;
  api: string;
  outcome: "replayed" | "live" | "miss";
  exchange?: number;
  message?: string;
}

/** What the replay session served, as the engine reports it. */
export interface ReplaySummary {
  fixture: string;
  mode: ReplayMode;
  replayed: number;
  live: number;
  misses: number;
  unused: number;
  live_cost_usd: number;
  unpriced_live_calls: number;
  records: ReplayRecord[];
}

/** A strict-mode replay request matched no recording. `message` is the engine's miss message. */
export class ReplayMissError extends Error {
  constructor(
    message: string,
    readonly summary: ReplaySummary,
  ) {
    super(message);
    this.name = "ReplayMissError";
  }
}

export class ReplayError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ReplayError";
  }
}

export interface ReplayOptions {
  /** Fixture directory (default `tests/attest/fixtures`). */
  fixturesDir?: string;
  /** `strict` or `auto`; default per `resolveReplayMode`. */
  mode?: ReplayMode;
  /** Engine binary (default: the SDK's engine discovery). */
  enginePath?: string;
}

export interface ReplaySession {
  readonly name: string;
  readonly mode: ReplayMode;
  /** The variables the session set in `process.env`. */
  readonly env: Record<string, string>;
  /** The summary once `finish()` has run, else `null`. */
  readonly summary: ReplaySummary | null;
  /** Stop the engine, restore the environment and return the summary. Idempotent. */
  finish(): Promise<ReplaySummary>;
}

/**
 * Replay mode from `explicit`, else `ATTEST_REPLAY` (`strict|auto|off`), else `strict`
 * when `CI` is set, else `auto`.
 */
export function resolveReplayMode(
  explicit?: ReplayModeSetting,
  env: NodeJS.ProcessEnv = process.env,
): ReplayModeSetting {
  if (explicit !== undefined) return checkMode(explicit, "mode");
  const fromEnv = env.ATTEST_REPLAY;
  if (fromEnv !== undefined && fromEnv !== "") return checkMode(fromEnv, "ATTEST_REPLAY");
  const ci = env.CI;
  return ci !== undefined && ci !== "" && ci !== "false" && ci !== "0" ? "strict" : "auto";
}

function checkMode(value: string, where: string): ReplayModeSetting {
  if (value === "strict" || value === "auto" || value === "off") return value;
  throw new ReplayError(`${where} must be strict, auto or off, got ${JSON.stringify(value)}`);
}

// startReplay() sets process-wide environment variables, so sessions cannot overlap.
let active: string | null = null;

/** Start a replay session for fixture `name`. Sessions cannot overlap. */
export async function startReplay(name: string, opts: ReplayOptions = {}): Promise<ReplaySession> {
  if (!NAME_PATTERN.test(name)) throw new ReplayError(`invalid fixture name ${JSON.stringify(name)}`);
  const mode = opts.mode ?? "strict";
  checkMode(mode, "mode");
  if (active !== null) throw new ReplayError(`replay of ${active} is already running; sessions cannot overlap`);
  active = name;
  try {
    return await start(name, mode, opts);
  } catch (err) {
    active = null;
    throw err;
  }
}

async function start(name: string, mode: ReplayMode, opts: ReplayOptions): Promise<ReplaySession> {
  const engine = await resolveEngine(opts.enginePath);
  const fixturesDir = path.resolve(opts.fixturesDir ?? DEFAULT_FIXTURE_DIR);
  const child = spawn(engine, ["replay", "--serve", "--name", name, "--fixtures-dir", fixturesDir, "--mode", mode], {
    stdio: ["pipe", "pipe", "pipe"],
  });
  let stdout = "";
  let stderr = "";
  let resolveReady: (line: string | null) => void = () => undefined;
  const ready = new Promise<string | null>((resolve) => {
    resolveReady = resolve;
  });
  const exited = new Promise<number | null>((resolve) => {
    child.on("close", (code) => {
      resolveReady(null);
      resolve(code);
    });
    child.on("error", (err) => {
      stderr += err.message;
      resolveReady(null);
      resolve(null);
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
  child.stdin.on("error", () => undefined);

  const stop = async (): Promise<{ code: number | null; stdout: string; stderr: string }> => {
    child.stdin.write("stop\n");
    child.stdin.end();
    return { code: await exited, stdout, stderr };
  };

  const env = parseEnv(await ready);
  if (env === null) {
    const out = await stop();
    active = null;
    throw new ReplayError(`attest-engine replay --serve did not start: ${out.stderr.trim() || `exit ${String(out.code)}`}`);
  }
  const saved = new Map<string, string | undefined>(Object.keys(env).map((k) => [k, process.env[k]]));
  Object.assign(process.env, env);

  let summary: ReplaySummary | null = null;
  let finishing: Promise<ReplaySummary> | null = null;
  const finish = (): Promise<ReplaySummary> => {
    finishing ??= (async () => {
      for (const [k, v] of saved) {
        if (v === undefined) delete process.env[k];
        else process.env[k] = v;
      }
      const out = await stop();
      active = null;
      const parsed = parseSummary(out.stdout);
      if (parsed === null) {
        throw new ReplayError(`replay ${name} wrote no summary (exit ${String(out.code)}): ${out.stderr.trim()}`);
      }
      summary = parsed;
      return parsed;
    })();
    return finishing;
  };
  return {
    name,
    mode,
    env,
    get summary() {
      return summary;
    },
    finish,
  };
}

function parseEnv(line: string | null): Record<string, string> | null {
  if (line === null) return null;
  try {
    const env = (JSON.parse(line) as { env?: unknown }).env;
    if (typeof env !== "object" || env === null) return null;
    const entries = Object.entries(env);
    if (!entries.every(([, v]) => typeof v === "string")) return null;
    return Object.fromEntries(entries) as Record<string, string>;
  } catch {
    return null;
  }
}

function parseSummary(out: string): ReplaySummary | null {
  const lines = out.trim().split("\n");
  try {
    const s = (JSON.parse(lines[lines.length - 1] ?? "") as { summary?: ReplaySummary }).summary;
    return s !== undefined && Array.isArray(s.records) ? s : null;
  } catch {
    return null;
  }
}

/** The first miss message of a summary, or `null`. */
export function firstMiss(summary: ReplaySummary): string | null {
  return summary.records.find((r) => r.outcome === "miss")?.message ?? null;
}

/** `N live model calls · $X.XX`, plus ` (+N unpriced)`. */
export function liveCallsLine(live: number, costUsd: number, unpriced = 0): string {
  const line = `${String(live)} live model ${live === 1 ? "call" : "calls"} · $${costUsd.toFixed(2)}`;
  return unpriced > 0 ? `${line} (+${String(unpriced)} unpriced)` : line;
}
