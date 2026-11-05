import * as https from "node:https";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";
import * as crypto from "node:crypto";
import { fileURLToPath } from "node:url";
import { ENGINE_VERSION, VERSION } from "./version.js";

const RELEASE_BASE = `https://github.com/attest-framework/attest/releases/download/v${ENGINE_VERSION}`;
const TABLE_NAME = "engine-checksums.json";
const MAX_REDIRECTS = 5;
const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308]);
const DEFAULT_CONNECT_TIMEOUT_MS = 10_000;
const DEFAULT_TOTAL_TIMEOUT_MS = 120_000;
const SHA256_HEX = /^[0-9a-f]{64}$/;

/**
 * Seams for tests. Production calls `downloadEngine()` with none of them. Every option keeps
 * https-only and certificate verification on; `ca` adds a trusted root, it never disables checks.
 */
export interface DownloadOptions {
  /** Release asset base URL (default: GitHub Releases for `ENGINE_VERSION`). */
  releaseBase?: string;
  /** Path of the embedded hash table (default: the one shipped in this package). */
  tablePath?: string;
  /** Extra trusted root certificate (PEM). */
  ca?: string | Buffer;
  /** Connect timeout, and the longest silence tolerated mid-transfer. */
  connectTimeoutMs?: number;
  /** Deadline for one whole fetch including redirects and body. */
  totalTimeoutMs?: number;
}

/**
 * Cache directory for this SDK's engine: `~/.attest/bin/<ENGINE_VERSION>/`.
 *
 * The directory name is the canonical engine version (for example `1.0.0`). The legacy
 * unversioned `~/.attest/bin/attest-engine` is neither read nor modified.
 */
export function versionedBinDir(): string {
  return path.join(os.homedir(), ".attest", "bin", ENGINE_VERSION);
}

export function platformKey(): string {
  const platform = os.platform();
  const arch = os.arch();

  const mapping: Record<string, Record<string, string>> = {
    darwin: { arm64: "darwin-arm64", x64: "darwin-amd64" },
    linux: { arm64: "linux-arm64", x64: "linux-amd64" },
    win32: { x64: "windows-amd64", arm64: "windows-arm64" }
  };

  const osMap = mapping[platform];
  if (!osMap) {
    throw new Error(
      `Unsupported platform: ${platform}. Supported: darwin, linux, win32.`
    );
  }

  const key = osMap[arch];
  if (!key) {
    throw new Error(
      `Unsupported architecture '${arch}' on platform '${platform}'. ` +
      `Supported architectures: ${Object.keys(osMap).join(", ")}.`
    );
  }

  return key;
}

export function binaryFilename(): string {
  return os.platform() === "win32" ? "attest-engine.exe" : "attest-engine";
}

/** The engine binary in the versioned cache, or null if it is absent. */
export function cachedEnginePath(): string | null {
  const binPath = path.join(versionedBinDir(), binaryFilename());
  return fs.statSync(binPath, { throwIfNoEntry: false })?.isFile() ? binPath : null;
}

function packageRoot(): string {
  let dir = path.dirname(fileURLToPath(import.meta.url));
  while (!fs.existsSync(path.join(dir, "package.json"))) {
    const parent = path.dirname(dir);
    if (parent === dir) return dir;
    dir = parent;
  }
  return dir;
}

function embeddedHash(key: string, tablePath: string): string {
  if (!fs.existsSync(tablePath)) {
    throw new Error(
      `This @attest-ai/core install has no embedded engine hash table (${TABLE_NAME}), which is ` +
      `how a source or workspace install looks, so it never downloads an engine. ` +
      `Point ATTEST_ENGINE_PATH at an engine binary, or build one with \`make engine\` ` +
      `and set ATTEST_ENGINE_PATH=./bin/attest-engine.`
    );
  }
  let hash: unknown;
  try {
    const table = JSON.parse(fs.readFileSync(tablePath, "utf-8")) as Record<string, Record<string, unknown>>;
    hash = table[ENGINE_VERSION]?.[key];
  } catch (err) {
    throw new Error(
      `@attest-ai/core ${VERSION} ships an unreadable engine hash table at ${tablePath}: ${String(err)}`
    );
  }
  if (typeof hash !== "string" || !SHA256_HEX.test(hash)) {
    throw new Error(
      `@attest-ai/core ${VERSION} ships no valid engine hash for v${ENGINE_VERSION} on ${key} ` +
      `in ${tablePath}.`
    );
  }
  return hash;
}

function httpsGet(url: string, o: Required<Pick<DownloadOptions, "connectTimeoutMs" | "totalTimeoutMs">> & Pick<DownloadOptions, "ca">): Promise<Buffer> {
  return new Promise<Buffer>((resolve, reject) => {
    let current: import("node:http").ClientRequest | undefined;
    let done = false;
    const finish = (err: Error | null, data?: Buffer): void => {
      if (done) return;
      done = true;
      clearTimeout(deadline);
      if (err) {
        current?.destroy();
        reject(err);
      } else {
        resolve(data as Buffer);
      }
    };
    const deadline = setTimeout(
      () => finish(new Error(`Timed out after ${o.totalTimeoutMs}ms fetching ${url}`)),
      o.totalTimeoutMs,
    );

    const hop = (target: string, redirects: number): void => {
      if (redirects > MAX_REDIRECTS) {
        finish(new Error(`Too many redirects (>${MAX_REDIRECTS}) following ${url}`));
        return;
      }
      let parsed: URL;
      try {
        parsed = new URL(target);
      } catch {
        finish(new Error(`Invalid URL: ${target}`));
        return;
      }
      if (parsed.protocol !== "https:") {
        finish(new Error(`Refusing to download over a non-https URL: ${target}`));
        return;
      }
      current = https.get(parsed, { ca: o.ca, timeout: o.connectTimeoutMs }, (res) => {
        const status = res.statusCode ?? 0;
        if (REDIRECT_STATUSES.has(status)) {
          res.resume();
          const location = res.headers.location;
          if (!location) {
            finish(new Error(`Redirect ${status} without Location header from ${target}`));
            return;
          }
          hop(new URL(location, parsed).toString(), redirects + 1);
          return;
        }
        if (status !== 200) {
          res.resume();
          finish(new Error(`HTTP ${status} fetching ${target}`));
          return;
        }
        const chunks: Buffer[] = [];
        res.on("data", (chunk: Buffer) => chunks.push(chunk));
        res.on("end", () => finish(null, Buffer.concat(chunks)));
        res.on("error", (err) => finish(err));
      });
      current.on("timeout", () =>
        finish(new Error(`No response within ${o.connectTimeoutMs}ms from ${target}`)),
      );
      current.on("error", (err) => finish(err));
    };
    hop(url, 0);
  });
}

export async function downloadEngine(options: DownloadOptions = {}): Promise<string> {
  const key = platformKey();
  const filename = `attest-engine-${key}${os.platform() === "win32" ? ".exe" : ""}`;
  const expectedHash = embeddedHash(key, options.tablePath ?? path.join(packageRoot(), TABLE_NAME));
  const binaryUrl = `${options.releaseBase ?? RELEASE_BASE}/${filename}`;

  process.stderr.write(`[attest] Downloading engine v${ENGINE_VERSION} for ${key}...\n`);
  let data: Buffer;
  try {
    data = await httpsGet(binaryUrl, {
      ca: options.ca,
      connectTimeoutMs: options.connectTimeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS,
      totalTimeoutMs: options.totalTimeoutMs ?? DEFAULT_TOTAL_TIMEOUT_MS,
    });
  } catch (err) {
    throw new Error(`Failed to download engine from ${binaryUrl}: ${(err as Error).message}`);
  }

  const actualHash = crypto.createHash("sha256").update(data).digest("hex");
  if (actualHash !== expectedHash) {
    throw new Error(
      `${filename} does not match the hash shipped in @attest-ai/core ${VERSION}: ` +
      `expected ${expectedHash}, got ${actualHash}. ` +
      `Refusing to install it. Retry, or set ATTEST_ENGINE_PATH to a binary you trust.`
    );
  }
  process.stderr.write(`[attest] Checksum verified.\n`);

  // Atomic write: temp file + rename
  const dir = versionedBinDir();
  fs.mkdirSync(dir, { recursive: true });
  const destPath = path.join(dir, binaryFilename());
  const suffix = `${String(process.pid)}.${crypto.randomBytes(8).toString("hex")}`;
  const tmpPath = path.join(dir, `.${binaryFilename()}.tmp.${suffix}`);
  try {
    fs.writeFileSync(tmpPath, data, { flag: "wx", mode: 0o755 });
    fs.chmodSync(tmpPath, 0o755);
    fs.renameSync(tmpPath, destPath);
  } catch (err) {
    // EEXIST means the temp name belongs to someone else; leave it alone.
    if ((err as NodeJS.ErrnoException).code !== "EEXIST") fs.rmSync(tmpPath, { force: true });
    throw err;
  }

  process.stderr.write(`[attest] Engine installed at ${destPath}\n`);
  return destPath;
}
