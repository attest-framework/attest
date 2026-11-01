/**
 * The `attest` CLI: `init` runs locally, every other command forwards to a matching engine.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import { main } from "../../packages/core/src/cli.js";
import { ENGINE_VERSION, VERSION } from "../../packages/core/src/version.js";
import {
  forwardedCalls,
  isolateEngineEnv,
  legacyCachePath,
  writeScriptedEngine,
  type IsolatedEngineEnv,
} from "../support/scripted-engine.js";

let env: IsolatedEngineEnv;
let stdout: string[];
let stderr: string[];

beforeEach(() => {
  env = isolateEngineEnv();
  stdout = [];
  stderr = [];
  vi.spyOn(process.stdout, "write").mockImplementation((chunk) => {
    stdout.push(String(chunk));
    return true;
  });
  vi.spyOn(process.stderr, "write").mockImplementation((chunk) => {
    stderr.push(String(chunk));
    return true;
  });
});

afterEach(() => {
  vi.restoreAllMocks();
  env.restore();
});

function useEngine(options: { version?: string; exitCode?: number } = {}): string {
  const engine = writeScriptedEngine(path.join(env.root, "e", "attest-engine"), options);
  process.env["ATTEST_ENGINE_PATH"] = engine;
  return engine;
}

describe("attest --help", () => {
  it("lists SDK and engine commands and exits 0", async () => {
    useEngine();

    await expect(main(["--help"])).resolves.toBe(0);

    const out = stdout.join("");
    expect(out).toContain("init");
    expect(out).toContain("doctor");
    expect(out).toContain("policy");
  });

  it("prints help for no arguments", async () => {
    useEngine();

    await expect(main([])).resolves.toBe(0);
    expect(stdout.join("")).toContain("SDK commands");
  });

  it("exits 1 when the engine help has no commands section", async () => {
    const engine = path.join(env.root, "bare", "attest-engine");
    fs.mkdirSync(path.dirname(engine));
    fs.writeFileSync(engine, `#!/bin/sh\necho 'attest-engine ${ENGINE_VERSION}'\n`, { mode: 0o755 });
    process.env["ATTEST_ENGINE_PATH"] = engine;

    await expect(main(["--help"])).resolves.toBe(1);
    expect(stderr.join("")).toContain("no commands section");
  });

  it("exits 1 when no engine can be resolved", async () => {
    await expect(main(["--help"])).resolves.toBe(1);
    expect(stderr.join("")).toContain("ATTEST_ENGINE_NO_DOWNLOAD");
  });
});

describe("attest --version", () => {
  it("prints the SDK version and the required engine version", async () => {
    await expect(main(["--version"])).resolves.toBe(0);
    expect(stdout.join("")).toContain(VERSION);
    expect(stdout.join("")).toContain(ENGINE_VERSION);
  });
});

describe("attest init", () => {
  let originalCwd: string;

  beforeEach(() => {
    originalCwd = process.cwd();
    process.chdir(env.root);
  });

  afterEach(() => {
    process.chdir(originalCwd);
  });

  it("creates tests directory and sample test file without an engine", async () => {
    await expect(main(["init"])).resolves.toBe(0);

    const sample = path.join(env.root, "tests", "test_my_agent.test.ts");
    expect(fs.readFileSync(sample, "utf-8")).toContain("@attest-ai/core");
  });

  it("does not scaffold when given extra arguments", async () => {
    await expect(main(["init", "--help"])).resolves.toBe(0);
    await expect(main(["init", "somedir"])).resolves.toBe(2);
    expect(fs.existsSync(path.join(env.root, "tests"))).toBe(false);
  });

  it("does not overwrite an existing test file", async () => {
    const sample = path.join(env.root, "tests", "test_my_agent.test.ts");
    fs.mkdirSync(path.dirname(sample), { recursive: true });
    fs.writeFileSync(sample, "// custom test\n");

    await expect(main(["init"])).resolves.toBe(0);
    expect(fs.readFileSync(sample, "utf-8")).toBe("// custom test\n");
  });
});

describe("forwarded commands", () => {
  it("forwards policy arguments and propagates the exit code", async () => {
    const engine = useEngine({ exitCode: 2 });

    const code = await main(["policy", "evaluate", "--policy", "p.yaml", "--report", "r.json"]);

    expect(code).toBe(2);
    expect(forwardedCalls(engine)).toEqual(["policy evaluate --policy p.yaml --report r.json"]);
  });

  it("forwards doctor --json", async () => {
    const engine = useEngine();

    await expect(main(["doctor", "--json"])).resolves.toBe(0);
    expect(forwardedCalls(engine)).toEqual(["doctor --json"]);
  });

  it("exits 1 without running an old engine", async () => {
    const engine = useEngine({ version: "0.8.0" });

    const code = await main(["policy", "evaluate", "--policy", "p.yaml", "--report", "r.json"]);

    expect(code).toBe(1);
    expect(forwardedCalls(engine)).toEqual([]);
    expect(stderr.join("")).toContain(
      `reports version 0.8.0; this SDK requires attest-engine ${ENGINE_VERSION}`,
    );
  });

  it("exits 1 when only a legacy unversioned engine is cached", async () => {
    const legacy = writeScriptedEngine(legacyCachePath(env.home), { version: "0.8.0" });

    await expect(main(["policy", "evaluate"])).resolves.toBe(1);
    expect(forwardedCalls(legacy)).toEqual([]);
  });

  it("rejects unknown options with exit 2", async () => {
    await expect(main(["--bogus"])).resolves.toBe(2);
    expect(stderr.join("")).toContain("unknown option --bogus");
  });
});
