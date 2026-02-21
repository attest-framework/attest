import * as https from "node:https";
import * as http from "node:http";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";
import * as crypto from "node:crypto";
import { ENGINE_VERSION } from "./version.js";

const RELEASE_BASE = `https://github.com/attest-framework/attest/releases/download/v${ENGINE_VERSION}`;
const CHECKSUMS_URL = `${RELEASE_BASE}/checksums-sha256.txt`;
const MAX_REDIRECTS = 5;

export function attestBinDir(): string {
  const dir = path.join(os.homedir(), ".attest", "bin");
  fs.mkdirSync(dir, { recursive: true });
  return dir;
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

export function cachedEnginePath(): string | null {
  const dir = path.join(os.homedir(), ".attest", "bin");
  const binPath = path.join(dir, binaryFilename());
  const versionFile = path.join(dir, ".engine-version");

  if (!fs.existsSync(binPath)) {
    return null;
  }

  if (!fs.existsSync(versionFile)) {
    return null;
  }

  const cachedVersion = fs.readFileSync(versionFile, "utf-8").trim();
  if (cachedVersion !== ENGINE_VERSION) {
    return null;
  }

  return binPath;
}

export function parseChecksums(text: string): Map<string, string> {
  const checksums = new Map<string, string>();
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    // Format: "{hash}  {filename}" (two spaces between hash and filename)
    const match = trimmed.match(/^([a-f0-9]{64})\s+(.+)$/);
    if (match) {
      checksums.set(match[2], match[1]);
    }
  }
  return checksums;
}

function httpsGet(url: string, redirectCount = 0): Promise<Buffer> {
  if (redirectCount > MAX_REDIRECTS) {
    return Promise.reject(
      new Error(`Too many redirects (>${MAX_REDIRECTS}) following ${url}`)
    );
  }

  return new Promise<Buffer>((resolve, reject) => {
    const client = url.startsWith("https:") ? https : http;
    client.get(url, (res) => {
      const status = res.statusCode ?? 0;

      if (status === 301 || status === 302) {
        const location = res.headers.location;
        if (!location) {
          reject(new Error(`Redirect ${status} without Location header from ${url}`));
          return;
        }
        res.resume();
        resolve(httpsGet(location, redirectCount + 1));
        return;
      }

      if (status < 200 || status >= 300) {
        res.resume();
        reject(new Error(`HTTP ${status} fetching ${url}`));
        return;
      }

      const chunks: Buffer[] = [];
      res.on("data", (chunk: Buffer) => chunks.push(chunk));
      res.on("end", () => resolve(Buffer.concat(chunks)));
      res.on("error", reject);
    }).on("error", reject);
  });
}

export async function downloadEngine(): Promise<string> {
  const key = platformKey();
  const filename = `attest-engine-${key}`;
  const binaryUrl = `${RELEASE_BASE}/${filename}`;

  process.stderr.write(`[attest] Downloading engine v${ENGINE_VERSION} for ${key}...\n`);

  // Fetch checksums
  process.stderr.write(`[attest] Fetching checksums...\n`);
  const checksumsText = (await httpsGet(CHECKSUMS_URL)).toString("utf-8");
  const checksums = parseChecksums(checksumsText);
  const expectedHash = checksums.get(filename);
  if (!expectedHash) {
    throw new Error(
      `No checksum found for '${filename}' in checksums-sha256.txt. ` +
      `Available files: ${[...checksums.keys()].join(", ")}`
    );
  }

  // Download binary
  process.stderr.write(`[attest] Downloading ${binaryUrl}...\n`);
  const data = await httpsGet(binaryUrl);

  // Verify SHA256
  const actualHash = crypto.createHash("sha256").update(data).digest("hex");
  if (actualHash !== expectedHash) {
    throw new Error(
      `SHA256 mismatch for ${filename}. ` +
      `Expected: ${expectedHash}, got: ${actualHash}. ` +
      `The download may be corrupted. Delete ~/.attest/bin/ and retry.`
    );
  }
  process.stderr.write(`[attest] Checksum verified.\n`);

  // Atomic write: temp file + rename
  const dir = attestBinDir();
  const destFilename = binaryFilename();
  const destPath = path.join(dir, destFilename);
  const tmpPath = path.join(dir, `.${destFilename}.tmp.${process.pid}`);

  fs.writeFileSync(tmpPath, data);
  fs.chmodSync(tmpPath, 0o755);
  fs.renameSync(tmpPath, destPath);

  // Write version marker
  fs.writeFileSync(path.join(dir, ".engine-version"), ENGINE_VERSION);

  process.stderr.write(`[attest] Engine installed at ${destPath}\n`);
  return destPath;
}
