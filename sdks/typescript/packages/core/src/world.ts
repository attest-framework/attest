/**
 * World: a stateful HTTP stand-in for the system an agent acts on.
 *
 * `attest-engine world --file world.yaml --serve` hosts the world on 127.0.0.1 and prints
 * the environment variable (for example `PAYMENTS_BASE_URL`) the agent's HTTP client reads.
 * {@link startWorld} starts it and sets that variable. When the session stops the engine
 * prints the final world-v1 snapshot; {@link WorldSession.state} reads the current one while
 * it runs. Pass either to `expect(trace).world(...)` to assert on the effects. In Vitest,
 * `useWorld` wraps this; it composes with `useReplay`.
 */
import { spawn } from "node:child_process";
import * as path from "node:path";
import { resolveEngine } from "./engine-manager.js";
import { escapeControl, parseEnv } from "./replay.js";

/** The path under the world's base URL that serves the current snapshot. */
export const STATE_PATH = "/_attest/state";
const STATE_TIMEOUT_MS = 10_000;

/** A world-v1 snapshot. */
export type WorldSnapshot = Record<string, unknown>;

export class WorldError extends Error {
  constructor(message: string) {
    super(escapeControl(message));
    this.name = "WorldError";
  }
}

export interface WorldOptions {
  /** Engine binary (default: the SDK's engine discovery). */
  enginePath?: string;
}

export interface WorldSession {
  /** The absolute `world.yaml` path. */
  readonly path: string;
  /** The variable the session set in `process.env`: the world's base URL. */
  readonly env: Record<string, string>;
  /** The world's base URL. */
  readonly baseUrl: string;
  /** The current world-v1 snapshot, read from the running world. */
  state(): Promise<WorldSnapshot>;
  /** Stop the world, restore the environment and return the final snapshot. Idempotent. */
  stop(): Promise<WorldSnapshot>;
}

// startWorld() sets process-wide environment variables, so worlds cannot overlap.
let active: string | null = null;

/** Host the world defined by `worldFile` until the session stops. Worlds cannot overlap. */
export async function startWorld(worldFile: string, opts: WorldOptions = {}): Promise<WorldSession> {
  if (active !== null) throw new WorldError(`world ${active} is already running; worlds cannot overlap`);
  const file = path.resolve(worldFile);
  active = file;
  try {
    return await start(file, opts);
  } catch (err) {
    active = null;
    throw err;
  }
}

async function start(file: string, opts: WorldOptions): Promise<WorldSession> {
  const engine = await resolveEngine(opts.enginePath);
  const child = spawn(engine, ["world", "--file", file, "--serve"], { stdio: ["pipe", "pipe", "pipe"] });
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

  const halt = async (): Promise<{ code: number | null; stdout: string; stderr: string }> => {
    child.stdin.write("stop\n");
    child.stdin.end();
    return { code: await exited, stdout, stderr };
  };

  const env = parseEnv(await ready);
  if (env === null) {
    const out = await halt();
    throw new WorldError(`attest-engine world ${file} did not start: ${out.stderr.trim() || `exit ${String(out.code)}`}`);
  }
  const urls = Object.values(env);
  if (urls.length !== 1) {
    await halt();
    throw new WorldError(`attest-engine world ${file}: want one env variable, got ${JSON.stringify(env)}`);
  }
  const baseUrl = urls[0] as string;
  const saved = new Map<string, string | undefined>(Object.keys(env).map((k) => [k, process.env[k]]));
  Object.assign(process.env, env);

  let stopped = false;
  let stopping: Promise<WorldSnapshot> | null = null;
  const stop = (): Promise<WorldSnapshot> => {
    stopped = true;
    stopping ??= (async () => {
      for (const [k, v] of saved) {
        if (v === undefined) delete process.env[k];
        else process.env[k] = v;
      }
      const out = await halt();
      active = null;
      const final = parseFinal(out.stdout);
      if (out.code !== 0 || final === null) {
        throw new WorldError(`attest-engine world ${file} failed (exit ${String(out.code)}): ${out.stderr.trim()}`);
      }
      return final;
    })();
    return stopping;
  };
  const state = async (): Promise<WorldSnapshot> => {
    if (stopped) throw new WorldError(`world ${file} is stopped; use the snapshot stop() returned`);
    const url = baseUrl + STATE_PATH;
    let res: Response;
    try {
      res = await fetch(url, { signal: AbortSignal.timeout(STATE_TIMEOUT_MS) });
    } catch (err) {
      throw new WorldError(`GET ${url}: ${err instanceof Error ? err.message : String(err)}`);
    }
    if (res.status !== 200) throw new WorldError(`GET ${url}: HTTP ${String(res.status)}`);
    const body: unknown = await res.json().catch(() => {
      throw new WorldError(`GET ${url}: the body is not JSON`);
    });
    if (typeof body !== "object" || body === null || Array.isArray(body)) {
      throw new WorldError(`GET ${url}: the body is not a snapshot object`);
    }
    return body as WorldSnapshot;
  };
  return { path: file, env, baseUrl, state, stop };
}

function parseFinal(out: string): WorldSnapshot | null {
  const lines = out.trim().split("\n");
  try {
    const world = (JSON.parse(lines[lines.length - 1] ?? "") as { world?: unknown }).world;
    if (typeof world !== "object" || world === null || Array.isArray(world)) return null;
    return world as WorldSnapshot;
  } catch {
    return null;
  }
}
