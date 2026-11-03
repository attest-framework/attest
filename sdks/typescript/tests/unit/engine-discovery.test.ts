import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("node:https", async () => {
  const actual = await vi.importActual<typeof import("node:https")>("node:https");
  return { ...actual, get: vi.fn() };
});

import * as crypto from "node:crypto";
import * as fs from "node:fs";
import * as https from "node:https";
import * as path from "node:path";
import { PassThrough } from "node:stream";
import {
  cachedEnginePath,
  downloadEngine,
  platformKey,
  versionedBinDir,
} from "../../packages/core/src/engine-downloader.js";
import {
  EngineManager,
  EngineVersionMismatchError,
  engineVersion,
  resolveEngine,
} from "../../packages/core/src/engine-manager.js";
import { ENGINE_VERSION } from "../../packages/core/src/version.js";
import {
  isolateEngineEnv,
  legacyCachePath,
  versionedCachePath,
  writeScriptedEngine,
  type IsolatedEngineEnv,
} from "../support/scripted-engine.js";

let env: IsolatedEngineEnv;

beforeEach(() => {
  env = isolateEngineEnv();
});

afterEach(() => {
  env.restore();
  vi.mocked(https.get).mockReset();
});

describe("engineVersion", () => {
  it("parses `attest-engine <version>`", () => {
    const engine = writeScriptedEngine(path.join(env.root, "e", "attest-engine"), {
      version: "9.9.9",
    });
    expect(engineVersion(engine)).toBe("9.9.9");
  });

  it("returns null for a binary that prints no version", () => {
    const engine = writeScriptedEngine(path.join(env.root, "e", "attest-engine"), {
      version: "",
    });
    expect(engineVersion(engine)).toBeNull();
  });
});

describe("resolveEngine", () => {
  it("returns ATTEST_ENGINE_PATH when it reports ENGINE_VERSION", async () => {
    const engine = writeScriptedEngine(path.join(env.root, "e", "attest-engine"));
    process.env["ATTEST_ENGINE_PATH"] = engine;

    await expect(resolveEngine()).resolves.toBe(engine);
  });

  it("rejects an old engine at ATTEST_ENGINE_PATH with a version-mismatch error", async () => {
    const engine = writeScriptedEngine(path.join(env.root, "e", "attest-engine"), {
      version: "0.8.0",
    });
    process.env["ATTEST_ENGINE_PATH"] = engine;

    const err = await resolveEngine().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(EngineVersionMismatchError);
    const mismatch = err as EngineVersionMismatchError;
    expect(mismatch.found).toBe("0.8.0");
    expect(mismatch.required).toBe(ENGINE_VERSION);
    expect(mismatch.source).toBe("ATTEST_ENGINE_PATH");
  });

  it("rejects a missing ATTEST_ENGINE_PATH file", async () => {
    process.env["ATTEST_ENGINE_PATH"] = "/nonexistent/attest-engine";

    await expect(resolveEngine()).rejects.toThrow("ATTEST_ENGINE_PATH");
  });

  it("prefers an explicit path over ATTEST_ENGINE_PATH", async () => {
    const explicit = writeScriptedEngine(path.join(env.root, "explicit", "attest-engine"));
    process.env["ATTEST_ENGINE_PATH"] = writeScriptedEngine(
      path.join(env.root, "e", "attest-engine"),
      { version: "0.8.0" },
    );

    await expect(resolveEngine(explicit)).resolves.toBe(explicit);
  });

  it("does not skip a mismatched engine on PATH", async () => {
    writeScriptedEngine(path.join(env.pathDir, "attest-engine"), { version: "0.8.0" });
    writeScriptedEngine(versionedCachePath(env.home));

    await expect(resolveEngine()).rejects.toThrow("from PATH");
  });

  it("uses the versioned cache and leaves the legacy binary untouched", async () => {
    const legacy = writeScriptedEngine(legacyCachePath(env.home), { version: "0.8.0" });
    const legacyBytes = fs.readFileSync(legacy);
    const cached = writeScriptedEngine(versionedCachePath(env.home));

    await expect(resolveEngine()).resolves.toBe(cached);
    expect(fs.readFileSync(legacy)).toEqual(legacyBytes);
  });

  it("treats a directory at the cache path as no cached engine", () => {
    fs.mkdirSync(versionedCachePath(env.home), { recursive: true });

    expect(cachedEnginePath()).toBeNull();
  });

  it("ignores a legacy-only cache when download is disabled", async () => {
    writeScriptedEngine(legacyCachePath(env.home), { version: "0.8.0" });

    await expect(resolveEngine()).rejects.toThrow("ATTEST_ENGINE_NO_DOWNLOAD");
  });

  it("EngineManager.start rejects an old explicit engine before spawning it", async () => {
    const engine = writeScriptedEngine(path.join(env.root, "e", "attest-engine"), {
      version: "0.9.0",
    });

    await expect(new EngineManager({ enginePath: engine }).start()).rejects.toBeInstanceOf(
      EngineVersionMismatchError,
    );
  });
});

function serveRelease(binary: Buffer, digest: string): void {
  const asset = `attest-engine-${platformKey()}`;
  const base = `https://github.com/attest-framework/attest/releases/download/v${ENGINE_VERSION}`;
  const files = new Map<string, Buffer>([
    [`${base}/checksums-sha256.txt`, Buffer.from(`${digest}  ${asset}\n`)],
    [`${base}/${asset}`, binary],
  ]);
  vi.mocked(https.get).mockImplementation(((url: string, cb: (res: unknown) => void) => {
    const body = files.get(url);
    const res = Object.assign(new PassThrough(), {
      statusCode: body === undefined ? 404 : 200,
      headers: {},
    });
    process.nextTick(() => {
      cb(res);
      res.end(body ?? Buffer.alloc(0));
    });
    return { on: () => undefined };
  }) as unknown as typeof https.get);
}

describe("versioned engine cache", () => {
  it("lives at ~/.attest/bin/<ENGINE_VERSION>/", () => {
    expect(versionedBinDir()).toBe(path.join(env.home, ".attest", "bin", ENGINE_VERSION));
    expect(cachedEnginePath()).toBeNull();
  });

  it("downloadEngine writes the verified binary there and keeps the legacy binary", async () => {
    const legacy = writeScriptedEngine(legacyCachePath(env.home), { version: "0.8.0" });
    const legacyBytes = fs.readFileSync(legacy);
    const binary = Buffer.from("new engine");
    serveRelease(binary, crypto.createHash("sha256").update(binary).digest("hex"));
    vi.spyOn(process.stderr, "write").mockReturnValue(true);

    const target = await downloadEngine();

    expect(target).toBe(versionedCachePath(env.home));
    expect(fs.readFileSync(target)).toEqual(binary);
    expect(fs.statSync(target).mode & 0o111).not.toBe(0);
    expect(fs.readFileSync(legacy)).toEqual(legacyBytes);
    expect(fs.readdirSync(versionedBinDir())).toEqual(["attest-engine"]);
  });

  it("downloadEngine rejects a tampered binary and writes nothing", async () => {
    serveRelease(
      Buffer.from("tampered"),
      crypto.createHash("sha256").update("published").digest("hex"),
    );
    vi.spyOn(process.stderr, "write").mockReturnValue(true);

    await expect(downloadEngine()).rejects.toThrow("SHA256 mismatch");
    expect(cachedEnginePath()).toBeNull();
  });
});
