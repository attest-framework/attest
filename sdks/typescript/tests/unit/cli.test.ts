/**
 * The `attest` CLI: `init` runs locally, every other command forwards to a matching engine.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import { engineCommands, exitStatus, main } from "../../packages/core/src/cli.js";
import { ENGINE_VERSION, VERSION } from "../../packages/core/src/version.js";
import { DEMO_MARKER } from "../../packages/core/src/scaffold.js";
import {
  forwardedCalls,
  isolateEngineEnv,
  legacyCachePath,
  writeScriptedEngine,
  type IsolatedEngineEnv,
  type ScriptedEngineOptions,
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

function useEngine(options: ScriptedEngineOptions = {}): string {
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

  it("exits 0 with a note when no engine resolves, without downloading", async () => {
    delete process.env["ATTEST_ENGINE_NO_DOWNLOAD"];

    await expect(main(["--help"])).resolves.toBe(0);
    await expect(main([])).resolves.toBe(0);

    const out = stdout.join("");
    expect(out).toContain("init");
    expect(out).toContain("engine commands: not available");
    expect(fs.existsSync(path.join(env.home, ".attest"))).toBe(false);
  });

  it("exits 0 with a note for a mismatched engine", async () => {
    useEngine({ version: "0.8.0" });

    await expect(main(["--help"])).resolves.toBe(0);
    expect(stdout.join("")).toContain("reports version 0.8.0");
  });

  it("exits 0 with a note for an engine without a command registry", async () => {
    useEngine({ helpMode: "server" });

    await expect(main(["--help"])).resolves.toBe(0);
    expect(stdout.join("")).toContain("predates the command registry");
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

  const sample = () => path.join(env.root, "tests", "my-agent.test.ts");
  const workflow = () => path.join(env.root, ".github", "workflows", "attest.yml");

  it("writes the sample test, Vitest config and policy without an engine", async () => {
    await expect(main(["init"])).resolves.toBe(0);

    const test = fs.readFileSync(sample(), "utf-8");
    expect(test).toContain("await evaluate(chain, { context })");
    expect(test.split("\n").filter((l) => l.includes(DEMO_MARKER))).toEqual([
      expect.stringContaining('.outputContains("refund");'),
    ]);
    expect(fs.readFileSync(path.join(env.root, "vitest.config.ts"), "utf-8")).toContain(
      'import { AttestReporter } from "@attest-ai/vitest/reporter";',
    );
    expect(fs.readFileSync(path.join(env.root, "attest.policy.yaml"), "utf-8")).toContain(
      "max_hard_fails: 0",
    );
    expect(fs.existsSync(workflow())).toBe(false);
  });

  it.each([[["--ci", "github"]], [["--ci=github"]]])(
    "writes the merge-gate workflow for %j",
    async (args) => {
      await expect(main(["init", ...args])).resolves.toBe(0);

      const steps = fs.readFileSync(workflow(), "utf-8").split("      - ").slice(1);
      const byName = new Map(steps.map((s) => [s.split("\n")[0]!.replace(/^name: /, ""), s]));
      expect(byName.get("Run tests")).toContain("continue-on-error: true");
      expect(byName.get("Run tests")).toContain("npx vitest run --outputFile.attest=attest-report.json");
      expect(byName.get("Upload the Attest report")).toContain("if: always()");
      expect(byName.get("Evaluate the merge policy")).toContain("if: always()");
      expect(byName.get("Evaluate the merge policy")).toContain('"$code" -eq 2');
      expect(byName.get("Annotate the pull request")).toContain(`pr-annotate@v${VERSION}`);
      expect(steps.at(-1)).toContain("if: always() && steps.tests.outcome == 'failure'");
    },
  );

  it.each([[["--help"], 0], [["somedir"], 2], [["--ci"], 2], [["--ci", "gitlab"], 2]])(
    "does not scaffold for %j",
    async (args, code) => {
      await expect(main(["init", ...args])).resolves.toBe(code);
      expect(fs.existsSync(path.join(env.root, "tests"))).toBe(false);
    },
  );

  it("does not overwrite existing files", async () => {
    for (const file of [sample(), path.join(env.root, "vitest.config.ts"), workflow()]) {
      fs.mkdirSync(path.dirname(file), { recursive: true });
      fs.writeFileSync(file, "// mine\n");
    }

    await expect(main(["init", "--ci", "github"])).resolves.toBe(0);

    for (const file of [sample(), path.join(env.root, "vitest.config.ts"), workflow()]) {
      expect(fs.readFileSync(file, "utf-8")).toBe("// mine\n");
    }
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

  it("refuses a same-version engine without a command registry", async () => {
    const engine = useEngine({ helpMode: "server" });

    await expect(main(["doctor", "--json"])).resolves.toBe(1);
    expect(forwardedCalls(engine)).toEqual([]);
    expect(stderr.join("")).toContain("predates `attest doctor`");
  });

  it("refuses a command the engine does not list", async () => {
    const engine = useEngine({ commands: ["policy"] });

    await expect(main(["doctor", "--json"])).resolves.toBe(1);
    expect(forwardedCalls(engine)).toEqual([]);
    expect(stderr.join("")).toContain("predates `attest doctor`");
  });

  it("exits 128 + signal when the engine is killed", async () => {
    useEngine({ signal: "TERM" });

    await expect(main(["policy", "evaluate"])).resolves.toBe(128 + 15);
  });

  it("rejects unknown options with exit 2", async () => {
    await expect(main(["--bogus"])).resolves.toBe(2);
    expect(stderr.join("")).toContain("unknown option --bogus");
  });
});

describe("engineCommands", () => {
  it("returns null when help hangs past the timeout", () => {
    const engine = writeScriptedEngine(path.join(env.root, "e", "attest-engine"), {
      helpMode: "hang",
    });

    expect(engineCommands(engine, 300)).toBeNull();
  });

  it("parses command names and summaries", () => {
    const engine = writeScriptedEngine(path.join(env.root, "e", "attest-engine"));

    expect([...(engineCommands(engine) ?? new Map()).keys()]).toEqual(["doctor", "policy"]);
  });
});

describe("exitStatus", () => {
  it("passes exit codes through and maps signals to 128 + N", () => {
    expect(exitStatus(2, null)).toBe(2);
    expect(exitStatus(null, "SIGKILL")).toBe(137);
  });
});
