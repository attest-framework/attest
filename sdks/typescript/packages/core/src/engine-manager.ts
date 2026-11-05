import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { createInterface, type Interface as ReadlineInterface } from "node:readline";
import { existsSync, accessSync, statSync, constants as fsConstants } from "node:fs";
import { delimiter, join } from "node:path";
import { AttestClient, type BudgetReservation } from "./client.js";
import type { InitializeResult, Usage } from "./proto/types.js";
import { ENGINE_VERSION, VERSION } from "./version.js";
import { binaryFilename, cachedEnginePath, downloadEngine } from "./engine-downloader.js";

const ENGINE_BINARY_NAME = "attest-engine";
const VERSION_PROBE_TIMEOUT_MS = 10_000;
const DEFAULT_INITIALIZE_TIMEOUT_MS = 30_000;
const SHUTDOWN_TIMEOUT_MS = 5_000;
const STDERR_TAIL_BYTES = 4_096;
/** How long a failed pipe write waits for the exiting engine's own report (exit status and stderr). */
const EXIT_REPORT_GRACE_MS = 1_000;
const PIPE_WRITE_ERROR_CODES = new Set(["EPIPE", "ECONNRESET", "ERR_STREAM_DESTROYED"]);

function isPipeWriteError(err: unknown): boolean {
  for (let e: unknown = err; e instanceof Error; e = e.cause) {
    const code = (e as NodeJS.ErrnoException).code;
    if (code !== undefined && PIPE_WRITE_ERROR_CODES.has(code)) return true;
  }
  return false;
}

/** A discovered engine does not report the version this SDK requires. */
export class EngineVersionMismatchError extends Error {
  readonly enginePath: string;
  readonly source: string;
  readonly found: string | null;
  readonly required: string;

  constructor(enginePath: string, source: string, found: string | null, required: string) {
    const reported = found === null ? "reports no version" : `reports version ${found}`;
    super(
      `attest-engine at ${enginePath} (from ${source}) ${reported}; ` +
        `this SDK requires attest-engine ${required}. ` +
        "Point ATTEST_ENGINE_PATH at a matching engine, or remove the mismatched one " +
        `from PATH so the SDK can download ${required} into ~/.attest/bin/${required}/.`,
    );
    this.name = "EngineVersionMismatchError";
    this.enginePath = enginePath;
    this.source = source;
    this.found = found;
    this.required = required;
  }
}

/**
 * The version `<path> version` reports, or null if it reports none.
 *
 * Every engine since 0.4 answers `version` with `attest-engine <version>`. stdin is
 * closed so a binary that starts the JSON-RPC server instead reads EOF and exits.
 */
export function engineVersion(enginePath: string): string | null {
  const proc = spawnSync(enginePath, ["version"], {
    stdio: ["ignore", "pipe", "pipe"],
    encoding: "utf-8",
    timeout: VERSION_PROBE_TIMEOUT_MS,
  });
  if (proc.error) throw proc.error;
  const out = proc.stdout.trim();
  const prefix = `${ENGINE_BINARY_NAME} `;
  if (proc.status !== 0 || !out.startsWith(prefix)) return null;
  return out.slice(prefix.length).trim();
}

function onPath(): string | null {
  for (const dir of (process.env["PATH"] ?? "").split(delimiter)) {
    if (!dir) continue;
    const candidate = join(dir, binaryFilename());
    try {
      accessSync(candidate, fsConstants.X_OK);
    } catch {
      continue;
    }
    if (statSync(candidate).isFile()) return candidate;
  }
  return null;
}

function requireFile(enginePath: string, label: string): void {
  if (!existsSync(enginePath) || !statSync(enginePath).isFile()) {
    throw new Error(`${label}=${enginePath} does not exist or is not a file.`);
  }
}

async function locateEngine(
  explicitPath: string | undefined,
  allowDownload: boolean,
): Promise<[string, string]> {
  if (explicitPath) {
    requireFile(explicitPath, "Engine path");
    return [explicitPath, "explicit engine path"];
  }
  const envPath = process.env["ATTEST_ENGINE_PATH"];
  if (envPath) {
    requireFile(envPath, "ATTEST_ENGINE_PATH");
    return [envPath, "ATTEST_ENGINE_PATH"];
  }
  const found = onPath();
  if (found) return [found, "PATH"];
  const cached = cachedEnginePath();
  if (cached) return [cached, "engine cache"];

  if (!allowDownload) {
    throw new Error(
      `No '${ENGINE_BINARY_NAME}' ${ENGINE_VERSION} in ATTEST_ENGINE_PATH, PATH or ` +
        `~/.attest/bin/${ENGINE_VERSION}/.`,
    );
  }

  const noDownload = process.env["ATTEST_ENGINE_NO_DOWNLOAD"]?.toLowerCase();
  if (noDownload === "1" || noDownload === "true" || noDownload === "yes") {
    throw new Error(
      `Cannot find '${ENGINE_BINARY_NAME}' ${ENGINE_VERSION} and ATTEST_ENGINE_NO_DOWNLOAD is set.\n` +
        "Install options:\n" +
        "  1. Build from source: make engine, then set ATTEST_ENGINE_PATH\n" +
        `  2. Download release v${ENGINE_VERSION}: https://github.com/attest-framework/attest/releases\n` +
        "  3. Allow auto-download by unsetting ATTEST_ENGINE_NO_DOWNLOAD",
    );
  }
  return [await downloadEngine(), "download"];
}

/**
 * Find an engine whose version matches this SDK and return its path.
 *
 * The single discovery function behind `EngineManager` and the `attest` CLI. Order:
 * explicit path → `ATTEST_ENGINE_PATH` → `attest-engine` on `PATH` →
 * `~/.attest/bin/<ENGINE_VERSION>/` → download (unless `ATTEST_ENGINE_NO_DOWNLOAD` is set or
 * `allowDownload` is false).
 * The first engine found must report exactly `ENGINE_VERSION`; a mismatched engine is
 * never skipped in favor of the next location.
 */
export async function resolveEngine(
  explicitPath?: string,
  options: { allowDownload?: boolean } = {},
): Promise<string> {
  const [enginePath, source] = await locateEngine(explicitPath, options.allowDownload ?? true);
  const found = engineVersion(enginePath);
  if (found !== ENGINE_VERSION) {
    throw new EngineVersionMismatchError(enginePath, source, found, ENGINE_VERSION);
  }
  return enginePath;
}

/** Session budget limits read from the environment; null means unlimited. */
interface SessionBudget {
  readonly judgeCalls: number | null;
  readonly usd: number | null;
}

function envNumber(name: string): number | null {
  const raw = process.env[name];
  if (raw === undefined || raw === "") return null;
  const parsed = Number(raw);
  // A malformed value is the engine's startup error to report, not ours.
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null;
}

/**
 * Rethrow the engine's exit report (status and stderr tail) when it arrives within `graceMs`.
 * A pipe write fails before the child's 'close' fires; a grandchild holding stderr open can
 * delay 'close' indefinitely, hence the bound.
 */
async function reportExit(exited: Promise<never>, graceMs: number): Promise<void> {
  let timer: NodeJS.Timeout | undefined;
  const grace = new Promise<void>((done) => {
    timer = setTimeout(done, graceMs);
  });
  try {
    await Promise.race([exited, grace]);
  } finally {
    clearTimeout(timer);
  }
}

export class EngineManager {
  private enginePath: string;
  private readonly logLevel: string;
  private readonly initializeTimeoutMs: number;
  private process: ChildProcess | null = null;
  private reader: ReadlineInterface | null = null;
  private initialized = false;
  private initResult: InitializeResult | null = null;
  private routedClient: AttestClient | undefined;
  /** The running engine's stderr tail; each launch owns a fresh one so a killed engine cannot write into it. */
  private stderr: { text: string } = { text: "" };
  private restarting: Promise<void> | null = null;

  private configuredBudget: SessionBudget = { judgeCalls: null, usd: null };
  private usedJudgeCalls = 0;
  private usedUsd = 0;
  private heldJudgeCalls = 0;
  private heldUsd = 0;
  /** Spend the SDK cannot observe may have happened; a restart then leaves no budget. */
  private unknownSpend = false;

  constructor(options?: { enginePath?: string; logLevel?: string; initializeTimeoutMs?: number }) {
    this.enginePath = options?.enginePath ?? "";
    this.logLevel = options?.logLevel ?? "warn";
    this.initializeTimeoutMs = options?.initializeTimeoutMs ?? DEFAULT_INITIALIZE_TIMEOUT_MS;
  }

  /**
   * Start the engine and complete the `initialize` handshake. Rejects when the engine exits,
   * closes its pipes or fails to answer within the initialize timeout; the error carries the
   * tail of the engine's stderr.
   */
  async start(): Promise<InitializeResult> {
    this.enginePath = await resolveEngine(this.enginePath || undefined);
    this.configuredBudget = {
      judgeCalls: envNumber("ATTEST_MAX_JUDGE_CALLS"),
      usd: envNumber("ATTEST_BUDGET_USD"),
    };
    this.usedJudgeCalls = this.usedUsd = this.heldJudgeCalls = this.heldUsd = 0;
    this.unknownSpend = false;

    this.initResult = await this.launch(undefined);
    this.initialized = true;
    return this.initResult;
  }

  /** Spawn the engine and run `initialize` on it; the engine is stopped when that fails. */
  private async launch(envOverrides: Record<string, string> | undefined): Promise<InitializeResult> {
    const stderr = { text: "" };
    this.stderr = stderr;
    const child = spawn(this.enginePath, [`--log-level=${this.logLevel}`], {
      stdio: ["pipe", "pipe", "pipe"],
      env: { ...process.env, ...envOverrides },
    });
    this.process = child;
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk: string) => {
      stderr.text = (stderr.text + chunk).slice(-STDERR_TAIL_BYTES);
    });
    // A write to a dead engine fails through the write callback (launch() then waits for the
    // exit report below); the stream's own 'error' event would only duplicate that as an
    // uncaught exception.
    child.stdin.on("error", () => undefined);

    this.reader = createInterface({ input: child.stdout });
    const client = new AttestClient(this);
    this.routedClient = client;
    client.startReader();

    const exited = new Promise<never>((_, reject) => {
      child.once("error", (err) => reject(new Error(`attest-engine failed to start: ${err.message}`)));
      child.once("close", (code, signal) => {
        const how = signal !== null ? `signal ${signal}` : `exit code ${String(code)}`;
        reject(
          new Error(
            `attest-engine exited before it was ready (${how}).` +
              (stderr.text === "" ? "" : ` stderr:\n${stderr.text.trimEnd()}`),
          ),
        );
      });
    });
    // After a successful start this rejection is just the engine stopping.
    exited.catch(() => undefined);

    try {
      return await Promise.race([this.initialize(client), exited]);
    } catch (err) {
      await this.terminate();
      if (isPipeWriteError(err)) await reportExit(exited, EXIT_REPORT_GRACE_MS);
      throw err;
    }
  }

  /** Send initialize and check the engine's version and capabilities. */
  private async initialize(client: AttestClient): Promise<InitializeResult> {
    const init = (await client.sendWithoutRestart(
      "initialize",
      {
        sdk_name: "attest-typescript",
        sdk_version: VERSION,
        protocol_version: 1,
        required_capabilities: ["layers_1_4"],
        preferred_encoding: "json",
      },
      this.initializeTimeoutMs,
    )) as InitializeResult;
    if (init.engine_version !== ENGINE_VERSION) {
      throw new EngineVersionMismatchError(
        this.enginePath,
        "initialize",
        init.engine_version,
        ENGINE_VERSION,
      );
    }
    if (!init.compatible) {
      throw new Error(`Engine incompatible. Missing capabilities: ${JSON.stringify(init.missing)}`);
    }
    return init;
  }

  async stop(): Promise<void> {
    if (this.restarting !== null) await this.restarting.catch(() => undefined);
    if (this.process === null) return;

    if (this.initialized && this.routedClient !== undefined && this.isRunning) {
      try {
        await this.routedClient.sendWithoutRestart("shutdown", {}, SHUTDOWN_TIMEOUT_MS);
      } catch {
        // shutdown failed, will kill
      }
    }
    await this.terminate();
    this.initialized = false;
  }

  /** Close the pipes, reject pending requests and stop the engine process (SIGKILL when `force`). */
  private async terminate(force = false): Promise<void> {
    const child = this.process;
    this.routedClient?.stopReader();
    this.routedClient?.rejectPending(new Error("Engine stopped."));
    this.reader?.close();
    this.reader = null;
    this.process = null;
    if (child === null) return;

    if (child.exitCode === null && child.signalCode === null) {
      await new Promise<void>((res) => {
        const timer = force ? undefined : setTimeout(() => child.kill("SIGKILL"), 5000);
        child.once("exit", () => {
          clearTimeout(timer);
          res();
        });
        child.kill(force ? "SIGKILL" : "SIGTERM");
      });
    }
  }

  /**
   * Replace the engine after a request timed out: its late response can no longer be told
   * apart from a new one. The new engine gets the remaining session budget: the configured
   * limit minus the usage already returned, minus what the abandoned requests may still have
   * reserved. A restart that leaves no budget makes later model calls
   * `not_evaluated(budget_exhausted)`.
   */
  restartAfterTimeout(abandoned: readonly BudgetReservation[]): Promise<void> {
    for (const r of abandoned) {
      this.heldJudgeCalls += r.judgeCalls ?? Number.POSITIVE_INFINITY;
      this.heldUsd += r.usd ?? Number.POSITIVE_INFINITY;
    }
    this.restarting ??= this.restart().finally(() => {
      this.restarting = null;
    });
    return this.restarting;
  }

  /** An engine reply the SDK cannot account for (no usage, an RPC error, a dropped reply) may have spent budget. */
  markUnknownSpend(): void {
    this.unknownSpend = true;
  }

  private async restart(): Promise<void> {
    // The abandoned engine may sit in a slow model call; nothing it says is wanted any more.
    await this.terminate(true);
    const overrides: Record<string, string> = {};
    const { judgeCalls, usd } = this.configuredBudget;
    const unknown = this.unknownSpend ? Number.POSITIVE_INFINITY : 0;
    if (judgeCalls !== null) {
      overrides["ATTEST_MAX_JUDGE_CALLS"] = String(
        Math.max(0, Math.floor(judgeCalls - this.usedJudgeCalls - this.heldJudgeCalls - unknown)),
      );
    }
    if (usd !== null) {
      overrides["ATTEST_BUDGET_USD"] = String(Math.max(0, usd - this.usedUsd - this.heldUsd - unknown));
    }
    this.initResult = await this.launch(overrides);
  }

  /** Count the usage an evaluate_batch result reported against the session budget. */
  recordUsage(usage: Usage): void {
    this.usedJudgeCalls += usage.judge_calls;
    this.usedUsd += usage.cost_usd;
  }

  /** Resolves once an engine restart in progress has finished. */
  async whenReady(): Promise<void> {
    if (this.restarting !== null) await this.restarting;
  }

  /** Tail of the engine's stderr: the reason an engine died. */
  get stderrTail(): string {
    return this.stderr.text.trimEnd();
  }

  /** The client that routes this engine's responses by id; replaced when the engine restarts. */
  get client(): AttestClient | undefined {
    return this.routedClient;
  }

  async sendRequest(method: string, params: Record<string, unknown>): Promise<unknown> {
    if (!this.initialized && method !== "initialize") {
      throw new Error("Engine not initialized. Call start() first.");
    }
    if (this.routedClient === undefined) throw new Error("Engine process not started.");
    return this.routedClient.sendRequest(method, params);
  }

  get isRunning(): boolean {
    return this.process !== null && this.process.exitCode === null && this.process.signalCode === null;
  }

  /** Expose process for AttestClient reader loop access */
  get childProcess(): ChildProcess | null {
    return this.process;
  }

  /** Expose readline interface for AttestClient reader loop */
  get readlineInterface(): ReadlineInterface | null {
    return this.reader;
  }
}
