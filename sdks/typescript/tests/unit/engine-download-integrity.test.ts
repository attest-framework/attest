import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { execFileSync } from "node:child_process";
import * as crypto from "node:crypto";
import * as fs from "node:fs";
import * as http from "node:http";
import type { AddressInfo } from "node:net";
import * as https from "node:https";
import * as os from "node:os";
import * as path from "node:path";
import {
  cachedEnginePath,
  downloadEngine,
  platformKey,
  type DownloadOptions,
} from "../../packages/core/src/engine-downloader.js";
import { ENGINE_VERSION, VERSION } from "../../packages/core/src/version.js";
import { isolateEngineEnv, type IsolatedEngineEnv } from "../support/scripted-engine.js";

type Route = (res: http.ServerResponse) => void;

const ASSET = `attest-engine-${platformKey()}${os.platform() === "win32" ? ".exe" : ""}`;
const ASSET_PATH = `/releases/download/v${ENGINE_VERSION}/${ASSET}`;

const sha256 = (data: Buffer | string): string =>
  crypto.createHash("sha256").update(data).digest("hex");

let certDir: string;
let cert: string;
let httpsServer: https.Server;
let plainServer: http.Server;
let routes: Map<string, Route>;
let httpsHits: string[];
let plainHits: string[];
let env: IsolatedEngineEnv;
let tablePath: string;

function listen(server: http.Server | https.Server): Promise<number> {
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve((server.address() as AddressInfo).port));
  });
}

let httpsPort = 0;
let plainPort = 0;

beforeAll(async () => {
  // Minted before any test isolates PATH.
  certDir = fs.mkdtempSync(path.join(os.tmpdir(), "attest-tls-"));
  const key = path.join(certDir, "key.pem");
  cert = path.join(certDir, "cert.pem");
  execFileSync("openssl", [
    "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
    "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1",
    "-keyout", key, "-out", cert,
  ], { stdio: "pipe" });
  routes = new Map();
  httpsHits = [];
  plainHits = [];
  httpsServer = https.createServer(
    { key: fs.readFileSync(key), cert: fs.readFileSync(cert) },
    (req, res) => {
      httpsHits.push(req.url ?? "");
      const route = routes.get(req.url ?? "");
      if (route) route(res);
      else {
        res.statusCode = 404;
        res.end();
      }
    },
  );
  plainServer = http.createServer((req, res) => {
    plainHits.push(req.url ?? "");
    res.end("plain");
  });
  httpsPort = await listen(httpsServer);
  plainPort = await listen(plainServer);
});

afterAll(async () => {
  for (const server of [httpsServer, plainServer]) {
    server.closeAllConnections();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
  fs.rmSync(certDir, { recursive: true, force: true });
});

beforeEach(() => {
  env = isolateEngineEnv();
  routes.clear();
  httpsHits.length = 0;
  plainHits.length = 0;
  tablePath = path.join(env.root, "engine-checksums.json");
  vi.spyOn(process.stderr, "write").mockReturnValue(true);
});

afterEach(() => {
  env.restore();
  vi.restoreAllMocks();
});

const options = (extra: DownloadOptions = {}): DownloadOptions => ({
  releaseBase: `https://127.0.0.1:${httpsPort}/releases/download/v${ENGINE_VERSION}`,
  tablePath,
  ca: fs.readFileSync(cert),
  ...extra,
});

function shipTable(binary: Buffer | string): void {
  fs.writeFileSync(tablePath, JSON.stringify({ [ENGINE_VERSION]: { [platformKey()]: sha256(binary) } }));
}

function serve(binary: Buffer | string): void {
  routes.set(ASSET_PATH, (res) => res.end(binary));
}

describe("downloadEngine against a local https release", () => {
  it("installs a binary that matches the shipped hash", async () => {
    shipTable("engine");
    serve("engine");

    const target = await downloadEngine(options());

    expect(fs.readFileSync(target, "utf-8")).toBe("engine");
    expect(fs.statSync(target).mode & 0o111).not.toBe(0);
  });

  it("refuses a modified binary even when the release checksums file agrees with it", async () => {
    shipTable("published");
    serve("tampered");
    routes.set(`/releases/download/v${ENGINE_VERSION}/checksums-sha256.txt`, (res) =>
      res.end(`${sha256("tampered")}  ${ASSET}\n`),
    );

    await expect(downloadEngine(options())).rejects.toThrow(
      `does not match the hash shipped in @attest-ai/core ${VERSION}`,
    );
    expect(cachedEnginePath()).toBeNull();
    expect(httpsHits.some((hit) => hit.includes("checksums-sha256"))).toBe(false);
  });

  it("refuses an https to http redirect without contacting the http host", async () => {
    shipTable("engine");
    routes.set(ASSET_PATH, (res) => {
      res.statusCode = 302;
      res.setHeader("Location", `http://127.0.0.1:${plainPort}${ASSET_PATH}`);
      res.end();
    });

    await expect(downloadEngine(options())).rejects.toThrow("non-https");
    expect(plainHits).toEqual([]);
    expect(cachedEnginePath()).toBeNull();
  });

  it("follows a relative Location against the URL that returned it", async () => {
    shipTable("behind a relative redirect");
    routes.set(ASSET_PATH, (res) => {
      res.statusCode = 302;
      res.setHeader("Location", "../blobs/engine-blob");
      res.end();
    });
    routes.set("/releases/download/blobs/engine-blob", (res) => res.end("behind a relative redirect"));

    const target = await downloadEngine(options());

    expect(fs.readFileSync(target, "utf-8")).toBe("behind a relative redirect");
    expect(httpsHits).toEqual([ASSET_PATH, "/releases/download/blobs/engine-blob"]);
  });

  it("fails within the timeout when the server stalls", async () => {
    shipTable("engine");
    routes.set(ASSET_PATH, () => undefined);
    const started = Date.now();

    await expect(
      downloadEngine(options({ connectTimeoutMs: 500, totalTimeoutMs: 1000 })),
    ).rejects.toThrow("Failed to download engine");

    expect(Date.now() - started).toBeLessThan(5000);
    expect(cachedEnginePath()).toBeNull();
  });

  it("fails within the total timeout when the server trickles bytes", async () => {
    shipTable("engine");
    routes.set(ASSET_PATH, (res) => {
      res.writeHead(200, { "Content-Length": "1000" });
      const timer = setInterval(() => res.write("x"), 100);
      res.on("close", () => clearInterval(timer));
    });
    const started = Date.now();

    await expect(
      downloadEngine(options({ connectTimeoutMs: 500, totalTimeoutMs: 1000 })),
    ).rejects.toThrow("Timed out");

    expect(Date.now() - started).toBeLessThan(5000);
  });

  it("turns a malformed redirect Location into a download failure instead of crashing", async () => {
    shipTable("engine");
    routes.set(ASSET_PATH, (res) => {
      res.writeHead(302, { Location: "http://a b" });
      res.end();
    });

    await expect(downloadEngine(options())).rejects.toThrow(
      /Failed to download engine from .*Invalid redirect Location "http:\/\/a b"/,
    );
    expect(cachedEnginePath()).toBeNull();
  });

  it("names ATTEST_ENGINE_PATH and make engine when the install has no table, and never fetches", async () => {
    serve("engine");

    const failure = downloadEngine(options({ tablePath: path.join(env.root, "absent.json") }));

    await expect(failure).rejects.toThrow("ATTEST_ENGINE_PATH");
    await expect(failure).rejects.toThrow("make engine");
    expect(httpsHits).toEqual([]);
  });

  it.each([
    ["an empty table", {}],
    ["a table without this platform", { [ENGINE_VERSION]: {} }],
    ["a malformed hash", { [ENGINE_VERSION]: { [platformKey()]: "not-a-sha256" } }],
  ])("rejects %s without fetching", async (_name, table) => {
    fs.writeFileSync(tablePath, JSON.stringify(table));
    serve("engine");

    await expect(downloadEngine(options())).rejects.toThrow("@attest-ai/core");
    expect(httpsHits).toEqual([]);
  });
});
