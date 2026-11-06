/**
 * The `attest` CLI: `init` runs locally, every other command forwards to a matching engine.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { engineCommands, exitStatus, main } from "../../packages/core/src/cli.js";
import { ENGINE_VERSION, VERSION } from "../../packages/core/src/version.js";
import { ACTION_PINS, ANNOTATE_ENGINE_VERSION, DEMO_MARKER } from "../../packages/core/src/scaffold.js";
import { Ajv2020 } from "ajv/dist/2020.js";
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

  it("writes the sample test and policy, and no Vitest config, without an engine", async () => {
    await expect(main(["init"])).resolves.toBe(0);

    const test = fs.readFileSync(sample(), "utf-8");
    expect(test).toContain("await evaluate(chain, { context })");
    expect(test.split("\n").filter((l) => l.includes(DEMO_MARKER))).toEqual([
      expect.stringContaining('.outputContains("refund");'),
    ]);
    expect(fs.existsSync(path.join(env.root, "vitest.config.ts"))).toBe(false);
    expect(fs.readFileSync(path.join(env.root, "attest.policy.yaml"), "utf-8")).toContain(
      "max_hard_fails: 0",
    );
    expect(fs.existsSync(workflow())).toBe(false);
  });

  it.each([[["--ci", "github"]], [["--ci=github"]]])(
    "writes the merge-gate workflow for %j",
    async (args) => {
      await expect(main(["init", ...args])).resolves.toBe(0);

      const [attestJob, annotateJob] = fs.readFileSync(workflow(), "utf-8").split("\n  annotate:\n");
      const steps = (attestJob ?? "").split("      - ").slice(1);
      const byName = new Map(steps.map((s) => [s.split("\n")[0]!.replace(/^name: /, ""), s]));
      expect(byName.get("Run tests")).toContain("continue-on-error: true");
      expect(byName.get("Run tests")).toContain(
        "npx vitest run --reporter=default --reporter=@attest-ai/vitest/reporter " +
          "--outputFile.attest=attest-report.json",
      );
      const runTests = byName.get("Run tests") ?? "";
      expect(runTests).toContain('# env:\n        #   ATTEST_REPORT_TRACES: "1"');
      expect(runTests).not.toContain("\n        env:");
      expect(runTests).toContain("not redacted");
      expect(runTests).toContain("private repositories");
      expect(byName.get("Write the failure viewer")).toContain("if: always()");
      expect(byName.get("Write the failure viewer")).toContain(
        "npx attest view attest-report.json --no-open --out attest-report.html",
      );
      expect(byName.get("Upload the Attest report")).toContain("attest-report.html");
      expect(byName.get("Write the JUnit report")).toContain("if: always()");
      expect(byName.get("Write the JUnit report")).toContain(
        "npx attest report junit --report attest-report.json --out attest-junit.xml",
      );
      expect(byName.get("Upload the Attest report")).toContain("if: always()");
      expect(byName.get("Upload the Attest report")).toContain("attest-junit.xml");
      const text = fs.readFileSync(workflow(), "utf-8");
      expect(text).toContain("\npermissions:\n  contents: read\n\njobs:");
      expect(attestJob).not.toContain("pull-requests");
      expect(attestJob).toContain("    permissions:\n      contents: read\n    steps:");
      expect(annotateJob).toContain("    permissions:\n      contents: read\n      pull-requests: write\n");
      expect(text.split("pull-requests: write")).toHaveLength(2);
      expect(byName.get("Evaluate the merge policy")).toContain("if: always()");
      expect(byName.get("Evaluate the merge policy")).toContain('"$code" -eq 2');
      expect(annotateJob).toContain("needs: attest");
      expect(annotateJob).toContain(
        "pr-annotate@5d5b748b13da7ba034b3acfce631cee8fcae720c # v1.0.1",
      );
      expect(annotateJob).toContain(
        "github.event.pull_request.head.repo.full_name == github.repository",
      );
      expect(annotateJob).toContain("name: attest-report");
      expect(steps.at(-1)).toContain("if: always() && steps.tests.outcome == 'failure'");
      const uses = text.split("\n").filter((line) => /^\s*(- )?uses:/.test(line));
      expect(uses.length).toBeGreaterThan(0);
      for (const line of uses) expect(line).toMatch(/@[0-9a-f]{40} # v\d+\.\d+\.\d+$/);
      for (const [action, { sha, version }] of Object.entries(ACTION_PINS)) {
        if (text.includes(`${action}@`)) expect(text).toContain(`${action}@${sha} # ${version}`);
      }
    },
  );

  it.each([
    [["--help"], 0],
    [["somedir"], 2],
    [["--ci"], 2],
    [["--ci", "gitlab"], 2],
    [["--json", "--json"], 2],
    [["--skills", "--skills"], 2],
    [["--mcp", "--mcp"], 2],
  ])(
    "does not scaffold for %j",
    async (args, code) => {
      await expect(main(["init", ...args])).resolves.toBe(code);
      expect(fs.existsSync(path.join(env.root, "tests"))).toBe(false);
    },
  );

  it("fetches the engine in the annotate job, before pr-annotate and never in the test job", async () => {
    await expect(main(["init", "--ci", "github"])).resolves.toBe(0);

    const [testJob, annotate] = fs.readFileSync(workflow(), "utf-8").split("\n  annotate:\n");
    const setupUv = annotate.indexOf("astral-sh/setup-uv@");
    const fetch = annotate.indexOf("name: Fetch the attest engine");
    const prAnnotate = annotate.indexOf("pr-annotate@");
    expect(setupUv).toBeGreaterThanOrEqual(0);
    expect(setupUv).toBeLessThan(fetch);
    expect(fetch).toBeLessThan(prAnnotate);
    expect(annotate.slice(setupUv, fetch)).toContain("enable-cache: false");
    const step = annotate.slice(fetch, prAnnotate);
    expect(step).toContain("working-directory: ${{ runner.temp }}");
    expect(step).toContain(
      `uvx --no-config --from "attest-ai==${ANNOTATE_ENGINE_VERSION}" attest doctor`,
    );
    expect(annotate.slice(prAnnotate)).toContain(`engine-version: ${ANNOTATE_ENGINE_VERSION}\n`);
    expect(testJob).not.toContain("uvx");
  });

  it("derives the engine version from the pr-annotate pin tag", () => {
    expect(ACTION_PINS["attest-framework/attest/.github/actions/pr-annotate"].version).toBe(
      `v${ANNOTATE_ENGINE_VERSION}`,
    );
  });

  it("pins the same SHAs attest's own workflows use", () => {
    const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../..");
    const files = [
      ...fs.readdirSync(path.join(root, ".github", "workflows")).map((f) => path.join(".github", "workflows", f)),
      path.join(".github", "actions", "setup-attest", "action.yml"),
    ];
    const used = new Map<string, Set<string>>();
    for (const file of files) {
      const text = fs.readFileSync(path.join(root, file), "utf-8");
      for (const m of text.matchAll(/uses:\s*([\w.-]+\/[\w.-]+)(?:\/\S*)?@([0-9a-f]{40})/g)) {
        used.set(m[1], (used.get(m[1]) ?? new Set()).add(m[2]));
      }
    }
    const shared = Object.keys(ACTION_PINS).filter((action) => used.has(action));
    expect(shared).toEqual(
      expect.arrayContaining(["actions/checkout", "actions/setup-node", "astral-sh/setup-uv"]),
    );
    for (const action of shared) {
      const { sha } = ACTION_PINS[action as keyof typeof ACTION_PINS];
      expect([...used.get(action)!], action).toContain(sha);
    }
  });

  it("does not overwrite existing files", async () => {
    for (const file of [sample(), workflow()]) {
      fs.mkdirSync(path.dirname(file), { recursive: true });
      fs.writeFileSync(file, "// mine\n");
    }

    await expect(main(["init", "--ci", "github"])).resolves.toBe(0);

    for (const file of [sample(), workflow()]) {
      expect(fs.readFileSync(file, "utf-8")).toBe("// mine\n");
    }
  });
});

const initSchema = JSON.parse(
  fs.readFileSync(
    path.resolve(
      path.dirname(fileURLToPath(import.meta.url)),
      "../../../../proto/attest/v1/cli/init.schema.json",
    ),
    "utf-8",
  ),
) as Record<string, unknown>;
const validateInit = new Ajv2020({ strict: false }).compile(initSchema);

/**
 * An attest-engine stand-in whose help lists `skills` and whose `skills` command runs
 * `body` (shell builtins only: PATH is isolated) after recording its arguments.
 */
function useSkillsEngine(body: string): string {
  const engine = path.join(env.root, "e", "attest-engine");
  fs.mkdirSync(path.dirname(engine), { recursive: true });
  fs.writeFileSync(
    engine,
    [
      "#!/bin/sh",
      'case "$1" in',
      `  version) echo 'attest-engine ${ENGINE_VERSION}' ;;`,
      "  help) echo 'commands:'; echo '  skills  install agent skills' ;;",
      `  skills) echo "$*" > "$0.calls"; ${body} ;;`,
      "esac",
      "",
    ].join("\n"),
    { mode: 0o755 },
  );
  process.env["ATTEST_ENGINE_PATH"] = engine;
  return engine;
}

/**
 * An attest-engine stand-in whose help lists `mcp` and whose `mcp` command runs `body`
 * (shell builtins only: PATH is isolated) after recording its arguments.
 */
function useMcpEngine(body: string): string {
  const engine = path.join(env.root, "e", "attest-engine");
  fs.mkdirSync(path.dirname(engine), { recursive: true });
  fs.writeFileSync(
    engine,
    [
      "#!/bin/sh",
      'case "$1" in',
      `  version) echo 'attest-engine ${ENGINE_VERSION}' ;;`,
      "  help) echo 'commands:'; echo '  mcp  serve attest over MCP' ;;",
      `  mcp) echo "$*" > "$0.calls"; ${body} ;;`,
      "esac",
      "",
    ].join("\n"),
    { mode: 0o755 },
  );
  process.env["ATTEST_ENGINE_PATH"] = engine;
  return engine;
}

const MCP_DOC =
  "echo '{\"dir\":\"/x\",\"created\":[\".mcp.json\"],\"updated\":[\".cursor/mcp.json\"]," +
  "\"unchanged\":[\".codex/config.toml\"],\"conflict\":[]}'";

describe("attest init --json and --skills", () => {
  let originalCwd: string;
  let project: string;

  beforeEach(() => {
    originalCwd = process.cwd();
    project = path.join(env.root, "project");
    fs.mkdirSync(project);
    process.chdir(project);
  });

  afterEach(() => {
    process.chdir(originalCwd);
  });

  function initDoc(): Record<string, string[]> {
    expect(stdout).toHaveLength(1);
    const doc = JSON.parse(stdout[0]!) as Record<string, string[]>;
    expect(validateInit(doc), JSON.stringify(validateInit.errors)).toBe(true);
    return doc;
  }

  it("--ci github --json prints only the created paths", async () => {
    await expect(main(["init", "--ci", "github", "--json"])).resolves.toBe(0);

    expect(initDoc()).toEqual({
      created: [".github/workflows/attest.yml", "attest.policy.yaml", "tests/my-agent.test.ts"],
      updated: [],
      skipped: [],
    });
    expect(fs.existsSync(path.join(project, ".github", "workflows", "attest.yml"))).toBe(true);
  });

  it("--json reports existing files as skipped and leaves them alone", async () => {
    fs.writeFileSync(path.join(project, "attest.policy.yaml"), "# mine\n");

    await expect(main(["init", "--json"])).resolves.toBe(0);

    expect(initDoc()).toEqual({
      created: ["tests/my-agent.test.ts"],
      updated: [],
      skipped: ["attest.policy.yaml"],
    });
    expect(fs.readFileSync(path.join(project, "attest.policy.yaml"), "utf-8")).toBe("# mine\n");
  });

  it.each([
    ["pnpm-lock.yaml", ["pnpm", "exec", "attest", "mcp"]],
    [null, ["npx", "--no", "attest", "mcp"]],
  ])("--mcp --json merges the engine's paths (lockfile %s)", async (lockfile, launch) => {
    const engine = useMcpEngine(MCP_DOC);
    if (lockfile !== null) fs.writeFileSync(path.join(project, lockfile), "");

    await expect(main(["init", "--mcp", "--json"])).resolves.toBe(0);

    expect(initDoc()).toEqual({
      created: [".mcp.json", "attest.policy.yaml", "tests/my-agent.test.ts"],
      updated: [".cursor/mcp.json"],
      skipped: [".codex/config.toml"],
      conflict: [],
    });
    expect(fs.readFileSync(`${engine}.calls`, "utf-8").trim()).toBe(
      `mcp install --dir ${process.cwd()} --command ${JSON.stringify(launch)} --json`,
    );
  });

  it("--mcp reports a conflicting config under its own label", async () => {
    useMcpEngine(
      "echo '{\"dir\":\"/x\",\"created\":[],\"updated\":[],\"unchanged\":[],\"conflict\":[\".mcp.json\"]}'",
    );

    await expect(main(["init", "--mcp", "--json"])).resolves.toBe(0);

    const doc = initDoc();
    expect(doc.conflict).toEqual([".mcp.json"]);
    expect(doc.skipped).not.toContain(".mcp.json");
  });

  it("--mcp conflicts are not printed as unchanged", async () => {
    useMcpEngine(
      "echo '{\"dir\":\"/x\",\"created\":[],\"updated\":[],\"unchanged\":[],\"conflict\":[\".mcp.json\"]}'",
    );

    await expect(main(["init", "--mcp"])).resolves.toBe(0);

    const err = stderr.join("");
    expect(err).toContain("Conflict");
    expect(err).not.toContain(`Skipped (unchanged): ${path.join(process.cwd(), ".mcp.json")}`);
  });

  it("init without --mcp has no conflict key", async () => {
    await expect(main(["init", "--json"])).resolves.toBe(0);

    expect(initDoc()).not.toHaveProperty("conflict");
  });

  it("--mcp with an engine that fails exits 1 and scaffolds nothing", async () => {
    useMcpEngine("echo boom >&2; exit 3");

    await expect(main(["init", "--mcp", "--json"])).resolves.toBe(1);

    expect(stdout.join("")).toBe("");
    expect(stderr.join("")).toContain("mcp install");
    expect(fs.existsSync(path.join(project, "tests"))).toBe(false);
  });

  it("--skills --json merges the engine's paths", async () => {
    const engine = useSkillsEngine(
      "echo '{\"dir\":\"/x\",\"created\":[\".claude/skills/attest/SKILL.md\"]," +
        "\"updated\":[\"AGENTS.md\"],\"unchanged\":[\".agents/skills/attest/SKILL.md\"]}'",
    );

    await expect(main(["init", "--skills", "--ci=github", "--json"])).resolves.toBe(0);

    expect(initDoc()).toEqual({
      created: [
        ".claude/skills/attest/SKILL.md",
        ".github/workflows/attest.yml",
        "attest.policy.yaml",
        "tests/my-agent.test.ts",
      ],
      updated: ["AGENTS.md"],
      skipped: [".agents/skills/attest/SKILL.md"],
    });
    expect(fs.readFileSync(`${engine}.calls`, "utf-8").trim()).toBe(
      `skills install --dir ${process.cwd()} --json`,
    );
  });

  it("--skills without --json prints the skills lines before the scaffold", async () => {
    useSkillsEngine(
      "echo '{\"dir\":\"/x\",\"created\":[\"a/SKILL.md\"],\"updated\":[],\"unchanged\":[]}'",
    );

    await expect(main(["init", "--skills"])).resolves.toBe(0);

    const out = stdout.join("");
    expect(out).toContain(`Created: ${path.join(process.cwd(), "a", "SKILL.md")}`);
    expect(out.indexOf("a/SKILL.md")).toBeLessThan(out.indexOf("Next:"));
  });

  it.each([
    ["exit 3", "echo boom >&2; exit 3"],
    ["not json", "echo 'not json'"],
    ["missing keys", `echo '{"created":[]}'`],
  ])("--skills with an engine that fails (%s) exits 1 and scaffolds nothing", async (_n, body) => {
    useSkillsEngine(body);

    await expect(main(["init", "--skills", "--json"])).resolves.toBe(1);

    expect(stdout).toEqual([]);
    expect(stderr.join("")).toContain("skills install");
    expect(fs.existsSync(path.join(project, "tests"))).toBe(false);
  });

  it("--skills with an engine that lacks `skills` exits 1", async () => {
    useEngine({ commands: ["doctor"] });

    await expect(main(["init", "--skills"])).resolves.toBe(1);

    expect(stderr.join("")).toContain("predates `attest skills`");
    expect(fs.existsSync(path.join(project, "tests"))).toBe(false);
  });

  it("--skills --json without an engine exits 1 with empty stdout", async () => {
    await expect(main(["init", "--skills", "--json"])).resolves.toBe(1);

    expect(stdout).toEqual([]);
    expect(fs.existsSync(path.join(project, "tests"))).toBe(false);
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
