import { spawn, execSync, type ChildProcess } from "node:child_process";
import { createInterface, type Interface as ReadlineInterface } from "node:readline";
import { existsSync, accessSync, constants as fsConstants } from "node:fs";
import { resolve, join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import type { InitializeResult } from "./proto/types.js";
import { decodeResponse, encodeRequest, extractResult } from "./proto/codec.js";
import { VERSION } from "./version.js";
import { cachedEnginePath, downloadEngine } from "./engine-downloader.js";

const ENGINE_BINARY_NAME = "attest-engine";

async function findEngineBinary(): Promise<string> {
  // 1. Explicit env var override
  const envPath = process.env["ATTEST_ENGINE_PATH"];
  if (envPath) {
    if (!existsSync(envPath)) {
      throw new Error(
        `ATTEST_ENGINE_PATH is set to '${envPath}' but the file does not exist.`
      );
    }
    return envPath;
  }

  // 2. Check PATH via which/where
  try {
    const cmd = process.platform === "win32" ? "where" : "which";
    const found = execSync(`${cmd} ${ENGINE_BINARY_NAME}`, { encoding: "utf-8" }).trim();
    if (found) return found;
  } catch {
    // not on PATH
  }

  // 3. Cached download in ~/.attest/bin/
  const cached = cachedEnginePath();
  if (cached) return cached;

  // 4. Monorepo dev layout (../../bin/ relative to this file)
  // 5. CWD ./bin/
  const candidates = [
    join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..", "..", "..", "bin", ENGINE_BINARY_NAME),
    join(process.cwd(), "bin", ENGINE_BINARY_NAME)
  ];

  for (const candidate of candidates) {
    const resolved = resolve(candidate);
    try {
      accessSync(resolved, fsConstants.X_OK);
      if (existsSync(resolved)) return resolved;
    } catch {
      // not accessible
    }
  }

  // 6. Auto-download
  const noDownload = process.env["ATTEST_ENGINE_NO_DOWNLOAD"];
  if (noDownload === "1" || noDownload === "true" || noDownload === "yes") {
    throw new Error(
      `Cannot find '${ENGINE_BINARY_NAME}' binary and auto-download is disabled ` +
      "(ATTEST_ENGINE_NO_DOWNLOAD is set). Install the engine manually:\n" +
      "  1. Download from https://github.com/attest-framework/attest/releases\n" +
      "  2. Place the binary on your PATH or set ATTEST_ENGINE_PATH"
    );
  }

  return downloadEngine();
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
    if (!this.enginePath) {
      this.enginePath = await findEngineBinary();
    }

    this.process = spawn(this.enginePath, [`--log-level=${this.logLevel}`], {
      stdio: ["pipe", "pipe", "pipe"],
    });

    this.reader = createInterface({ input: this.process.stdout! });

    const result = await this.sendRequestInternal("initialize", {
      sdk_name: "attest-typescript",
      sdk_version: VERSION,
      protocol_version: 1,
      required_capabilities: ["layers_1_4"],
      preferred_encoding: "json",
    });

    this.initResult = result as InitializeResult;
    if (!this.initResult.compatible) {
      throw new Error(
        `Engine incompatible. Missing capabilities: ${JSON.stringify(this.initResult.missing)}`,
      );
    }
    this.initialized = true;
    return this.initResult;
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
