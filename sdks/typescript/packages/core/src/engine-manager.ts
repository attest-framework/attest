import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { createInterface, type Interface as ReadlineInterface } from "node:readline";
import { existsSync, accessSync, statSync, constants as fsConstants } from "node:fs";
import { delimiter, join } from "node:path";
import type { InitializeResult } from "./proto/types.js";
import { decodeResponse, encodeRequest, extractResult } from "./proto/codec.js";
import { ENGINE_VERSION, VERSION } from "./version.js";
import { binaryFilename, cachedEnginePath, downloadEngine } from "./engine-downloader.js";

const ENGINE_BINARY_NAME = "attest-engine";
const VERSION_PROBE_TIMEOUT_MS = 10_000;

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

export class EngineManager {
  private enginePath: string;
  private readonly logLevel: string;
  private process: ChildProcess | null = null;
  private reader: ReadlineInterface | null = null;
  private initialized = false;
  private requestId = 0;
  private initResult: InitializeResult | null = null;

  constructor(options?: { enginePath?: string; logLevel?: string }) {
    this.enginePath = options?.enginePath ?? "";
    this.logLevel = options?.logLevel ?? "warn";
  }

  async start(): Promise<InitializeResult> {
    this.enginePath = await resolveEngine(this.enginePath || undefined);

    this.process = spawn(this.enginePath, [`--log-level=${this.logLevel}`], {
      stdio: ["pipe", "pipe", "pipe"],
    });

    this.reader = createInterface({ input: this.process.stdout! });

    try {
      this.initResult = await this.initialize();
    } catch (err) {
      await this.stop();
      throw err;
    }
    this.initialized = true;
    return this.initResult;
  }

  /** Send initialize and check the engine's version and capabilities. */
  private async initialize(): Promise<InitializeResult> {
    const init = (await this.sendRequestInternal("initialize", {
      sdk_name: "attest-typescript",
      sdk_version: VERSION,
      protocol_version: 1,
      required_capabilities: ["layers_1_4"],
      preferred_encoding: "json",
    })) as InitializeResult;
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
    if (this.process === null) return;

    if (this.initialized) {
      try {
        await this.sendRequestInternal("shutdown", {});
      } catch {
        // shutdown failed, will kill
      }
    }

    if (this.reader) {
      this.reader.close();
      this.reader = null;
    }

    if (this.process.exitCode === null) {
      this.process.kill("SIGTERM");
      await new Promise<void>((res) => {
        const timer = setTimeout(() => {
          this.process?.kill("SIGKILL");
        }, 5000);
        this.process!.on("exit", () => {
          clearTimeout(timer);
          res();
        });
      });
    }

    this.initialized = false;
    this.process = null;
  }

  async sendRequest(method: string, params: Record<string, unknown>): Promise<unknown> {
    if (!this.initialized && method !== "initialize") {
      throw new Error("Engine not initialized. Call start() first.");
    }
    return this.sendRequestInternal(method, params);
  }

  private async sendRequestInternal(method: string, params: Record<string, unknown>): Promise<unknown> {
    if (this.process === null || this.process.stdin === null || this.reader === null) {
      throw new Error("Engine process not started.");
    }

    this.requestId += 1;
    const requestStr = encodeRequest(this.requestId, method, params);

    return new Promise<unknown>((resolve, reject) => {
      const onLine = (line: string): void => {
        try {
          const response = decodeResponse(line);
          resolve(extractResult(response));
        } catch (err) {
          reject(err);
        }
      };

      this.reader!.once("line", onLine);

      const ok = this.process!.stdin!.write(requestStr, (err: Error | null | undefined) => {
        if (err) {
          this.reader!.removeListener("line", onLine);
          reject(err);
        }
      });

      if (!ok) {
        this.process!.stdin!.once("drain", () => {
          // write will proceed after drain
        });
      }
    });
  }

  get isRunning(): boolean {
    return this.process !== null && this.process.exitCode === null;
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
