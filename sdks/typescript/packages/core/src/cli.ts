#!/usr/bin/env node
/**
 * The `attest` command line (`npx attest`).
 *
 * `attest init` runs in the SDK. Every other command (`doctor`, `baseline`, `policy`,
 * `cache`, `calibrate`, `version` and any command a later engine adds) runs in
 * `attest-engine`: the CLI resolves a version-checked engine with `resolveEngine`, checks
 * that the engine's `help` lists the command, and forwards the arguments, stdio and exit
 * code unchanged. A missing, mismatched or too-old engine exits 1 before anything runs, so
 * a gate such as `attest policy evaluate` never exits 0 without evaluating.
 *
 * `attest capture` is forwarded the same way; when the engine writes a fixture, the CLI
 * then drafts its Vitest file (`emitVitest`).
 */

import { spawnSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { ENGINE_VERSION, VERSION } from "./version.js";
import { resolveEngine } from "./engine-manager.js";
import { scaffoldProject } from "./scaffold.js";
import { emitVitest, testPathFor } from "./capture.js";

const HELP_FLAGS = new Set(["-h", "--help"]);

const SDK_USAGE =
  "usage: attest <command> [args]\n" +
  "\n" +
  "SDK commands:\n" +
  "  init [--ci github] [--skills] [--mcp] [--json]\n" +
  "                      scaffold a sample test and merge policy here\n" +
  "                      (--ci github also writes .github/workflows/attest.yml;\n" +
  "                      --skills installs the agent skills with attest-engine;\n" +
  "                      --mcp writes the MCP client configs (.mcp.json, .cursor/mcp.json,\n" +
  "                      .codex/config.toml) with attest-engine;\n" +
  "                      --json prints {created, updated, skipped} paths as JSON)\n" +
  "\n" +
  "`attest capture` (engine) also drafts tests/attest/<name>.test.ts from a fixture that\n" +
  "is consistent with the recording, unless that file exists. For a command capture the\n" +
  "test re-runs the command under strict replay (egress blocked) and evaluates the\n" +
  "contracts on the new run.\n" +
  "\n" +
  "options:\n" +
  "  --version  print the SDK version and the engine version it requires\n" +
  "  --help     print this help\n";

const HELP_PROBE_TIMEOUT_MS = 10_000;

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * `{command: summary}` from `<engine> help`, or null if it lists none.
 *
 * stdin is closed and the probe times out after 10 s, so an engine that predates the
 * command registry (and starts the JSON-RPC server for `help`) cannot hang the CLI.
 */
export function engineCommands(
  engine: string,
  timeoutMs: number = HELP_PROBE_TIMEOUT_MS,
): Map<string, string> | null {
  const proc = spawnSync(engine, ["help"], {
    stdio: ["ignore", "pipe", "pipe"],
    encoding: "utf-8",
    timeout: timeoutMs,
  });
  if (proc.error || proc.status !== 0) return null;
  const lines = proc.stdout.split("\n");
  const start = lines.indexOf("commands:");
  if (start === -1) return null;
  const commands = new Map<string, string>();
  for (const line of lines.slice(start + 1)) {
    const match = /^\s*(\S+)\s*(.*)$/.exec(line);
    if (match?.[1]) commands.set(match[1], (match[2] ?? "").trim());
  }
  return commands;
}

/** Print SDK commands, plus engine commands when an engine is available without download. */
async function printHelp(): Promise<number> {
  process.stdout.write(`${SDK_USAGE}\n`);
  let engine: string;
  try {
    engine = await resolveEngine(undefined, { allowDownload: false });
  } catch (err) {
    const firstLine = (errorMessage(err).split("\n")[0] ?? "").replace(/\.$/, "");
    process.stdout.write(
      `engine commands: not available (${firstLine}). Run \`attest doctor\` to download ` +
        `attest-engine ${ENGINE_VERSION}, or set ATTEST_ENGINE_PATH.\n`,
    );
    return 0;
  }
  const commands = engineCommands(engine);
  if (commands === null) {
    process.stdout.write(
      `engine commands: not available (attest-engine at ${engine} lists no commands; ` +
        "it predates the command registry).\n",
    );
    return 0;
  }
  const width = Math.max(0, ...[...commands.keys()].map((name) => name.length));
  process.stdout.write(`engine commands (attest-engine ${ENGINE_VERSION}, ${engine}):\n`);
  for (const [name, summary] of commands) {
    process.stdout.write(`  ${name.padEnd(width)}  ${summary}\n`);
  }
  return 0;
}

/** Map a child's exit to a shell exit status (signal N → 128 + N). */
export function exitStatus(status: number | null, signal: NodeJS.Signals | null): number {
  if (status !== null) return status;
  if (signal !== null) return 128 + (os.constants.signals[signal] ?? 0);
  return 1;
}

/**
 * Resolve a version-checked engine whose `help` lists `command`; print the reason to
 * stderr and resolve to null when there is none.
 */
async function engineProviding(command: string): Promise<string | null> {
  let engine: string;
  try {
    engine = await resolveEngine();
  } catch (err) {
    process.stderr.write(`attest: ${errorMessage(err)}\n`);
    return null;
  }
  const commands = engineCommands(engine);
  if (commands === null || (command !== "help" && !commands.has(command))) {
    process.stderr.write(
      `attest: attest-engine at ${engine} (version ${ENGINE_VERSION}) predates ` +
        `\`attest ${command}\`: its help does not list that command. Set ATTEST_ENGINE_PATH ` +
        "to an engine built from this release (make engine), or install a release whose " +
        "engine provides it.\n",
    );
    return null;
  }
  return engine;
}

const INIT_HELP_ENTRY = {
  name: "init",
  summary: "scaffold a sample test and policy; --ci github, --skills, --mcp, --json",
  schema: "cli/init.schema.json",
  exit_codes: [
    { code: 0, meaning: "success" },
    {
      code: 1,
      meaning: "--skills or --mcp could not install: the engine is missing, too old or failed",
    },
    { code: 2, meaning: "usage error" },
  ],
};

/** Print the engine's `help --json` document with the SDK's `init` command added. */
export function helpJson(engine: string): number {
  const proc = spawnSync(engine, ["help", "--json"], {
    stdio: ["ignore", "pipe", "pipe"],
    encoding: "utf-8",
  });
  if (proc.error) throw proc.error;
  if (proc.status !== 0) {
    process.stderr.write(proc.stderr);
    return exitStatus(proc.status, proc.signal);
  }
  let doc: { commands: { name: string }[] };
  try {
    doc = JSON.parse(proc.stdout) as { commands: { name: string }[] };
    if (!Array.isArray(doc.commands)) throw new Error("no commands array");
  } catch (err) {
    process.stderr.write(`attest: \`${engine} help --json\` printed an unexpected document: ${errorMessage(err)}\n`);
    return 1;
  }
  doc.commands = [...doc.commands, INIT_HELP_ENTRY].sort((a, b) =>
    a.name < b.name ? -1 : a.name > b.name ? 1 : 0,
  );
  process.stdout.write(`${JSON.stringify(doc, null, 2)}\n`);
  return 0;
}

async function forward(args: string[]): Promise<number> {
  const command = args[0] ?? "";
  const engine = await engineProviding(command);
  if (engine === null) return 1;
  if (args.length === 2 && command === "help" && args[1] === "--json") return helpJson(engine);
  if (command === "capture" && captureWritesFixture(args.slice(1))) {
    return captureAndEmit(engine, args.slice(1));
  }
  const proc = spawnSync(engine, args, { stdio: "inherit" });
  if (proc.error) throw proc.error;
  return exitStatus(proc.status, proc.signal);
}

// Engine capture modes the CLI drafts no test for: help, --check-redaction (writes no
// fixture) and --serve (used by record(), which drafts its own). Go flags accept one or
// two dashes.
const CAPTURE_NO_TEST = new Set(["check-redaction", "serve", "h", "help"]);

function flagName(arg: string): string | null {
  if (!arg.startsWith("-") || arg === "-" || arg === "--") return null;
  return arg.replace(/^-+/, "").split("=", 1)[0] ?? null;
}

/** True when `attest capture` args select a mode whose fixture the CLI drafts a test for. */
export function captureWritesFixture(args: string[]): boolean {
  for (const a of args) {
    if (a === "--") break;
    const name = flagName(a);
    if (name !== null && CAPTURE_NO_TEST.has(name)) return false;
  }
  return true;
}

/** The `--world` file of `attest capture` args (before the `--` command), resolved against cwd. */
export function captureWorldPath(args: string[]): string | undefined {
  for (let i = 0; i < args.length; i++) {
    const a = args[i] as string;
    if (a === "--") return undefined;
    const [name, ...rest] = a.split("=");
    if (name === "--world" || name === "-world") {
      const value = rest.length > 0 ? rest.join("=") : args[i + 1];
      return value === undefined ? undefined : path.resolve(value);
    }
  }
  return undefined;
}

function userResultFile(args: string[]): boolean {
  for (const a of args) {
    if (a === "--") break;
    if (flagName(a) === "result-file") return true;
  }
  return false;
}

/** Run `attest-engine capture` and draft the Vitest file for the fixture it wrote. */
function captureAndEmit(engine: string, args: string[]): number {
  if (userResultFile(args)) {
    process.stderr.write(
      "attest: capture --result-file is set by the attest CLI; run attest-engine capture directly to choose it\n",
    );
    return 2;
  }
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "attest-capture-"));
  const resultFile = path.join(tmp, "result.json");
  let result: { fixture: string; verified: boolean; outcome: string };
  let code: number;
  try {
    const proc = spawnSync(engine, ["capture", "--result-file", resultFile, ...args], { stdio: "inherit" });
    if (proc.error) throw proc.error;
    code = exitStatus(proc.status, proc.signal);
    if (!fs.existsSync(resultFile)) return code;
    result = JSON.parse(fs.readFileSync(resultFile, "utf8")) as { fixture: string; verified: boolean; outcome: string };
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
  const fixture = result.fixture;
  if (!result.verified) {
    const reason = result.outcome === "nothing_to_verify" ? "with nothing to verify" : "for unverified";
    process.stderr.write(`  test: none drafted ${reason} ${fixture}\n`);
    return code;
  }
  const written = emitVitest(fixture, { cwd: process.cwd(), world: captureWorldPath(args) });
  process.stderr.write(
    written === null
      ? `  test: ${testPathFor(fixture)} exists; left unchanged\n`
      : `  test: drafted ${written}; for a command capture it re-runs the command under strict replay (egress blocked) and evaluates the contracts on the new run\n`,
  );
  return code;
}

const INIT_USAGE =
  "usage: attest init [--ci github] [--skills] [--mcp] [--json]  (scaffolds into the current directory)";

const INIT_KINDS = ["created", "updated", "skipped"] as const;
// `--mcp` adds a fourth kind: configs that already hold a different `attest` entry.
const INIT_KINDS_WITH_CONFLICT = [...INIT_KINDS, "conflict"] as const;
type InitKind = (typeof INIT_KINDS_WITH_CONFLICT)[number];
type InitResult = Record<InitKind, string[]>;

// `attest-engine skills install --json` key -> `attest init --json` key.
const SKILLS_KINDS = [
  ["created", "created"],
  ["updated", "updated"],
  ["unchanged", "skipped"],
] as const;

// `attest-engine mcp install --json` key -> `attest init --json` key.
const MCP_KINDS = [
  ["created", "created"],
  ["updated", "updated"],
  ["unchanged", "skipped"],
  ["conflict", "conflict"],
] as const;

interface InitArgs {
  ci: boolean;
  skills: boolean;
  mcp: boolean;
  json: boolean;
}

/** Parse `attest init` args; null on a usage error. */
function parseInitArgs(args: string[]): InitArgs | null {
  const parsed: InitArgs = { ci: false, skills: false, mcp: false, json: false };
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === "--ci" && args[i + 1] === "github" && !parsed.ci) {
      parsed.ci = true;
      i++;
    } else if (arg === "--ci=github" && !parsed.ci) {
      parsed.ci = true;
    } else if (arg === "--skills" && !parsed.skills) {
      parsed.skills = true;
    } else if (arg === "--mcp" && !parsed.mcp) {
      parsed.mcp = true;
    } else if (arg === "--json" && !parsed.json) {
      parsed.json = true;
    } else {
      return null;
    }
  }
  return parsed;
}

function emptyInitResult(): InitResult {
  return { created: [], updated: [], skipped: [], conflict: [] };
}

/**
 * Run `<engine> <subcommand> --json` and map its path lists to init kinds.
 * Throws when the engine fails or prints an unexpected document.
 */
function runEngineInstall(
  engine: string,
  subcommand: string[],
  kinds: readonly (readonly [string, InitKind])[],
  what: string,
): InitResult {
  const argv = [...subcommand, "--json"];
  const shown = [engine, ...argv].join(" ");
  const proc = spawnSync(engine, argv, {
    stdio: ["ignore", "pipe", "pipe"],
    encoding: "utf-8",
  });
  if (proc.error) throw proc.error;
  if (proc.status !== 0) {
    const detail = proc.stderr.trim();
    throw new Error(
      `${what} failed (\`${shown}\` exited ${String(exitStatus(proc.status, proc.signal))})` +
        (detail === "" ? "" : `: ${detail}`),
    );
  }
  process.stderr.write(proc.stderr);
  let doc: unknown;
  try {
    doc = JSON.parse(proc.stdout);
  } catch (err) {
    throw new Error(`\`${shown}\` printed invalid JSON: ${errorMessage(err)}`);
  }
  if (typeof doc !== "object" || doc === null || Array.isArray(doc)) {
    throw new Error(`\`${shown}\` printed ${JSON.stringify(doc)}, not an object`);
  }
  const record = doc as Record<string, unknown>;
  const result = emptyInitResult();
  for (const [engineKind, initKind] of kinds) {
    const paths = record[engineKind];
    if (!Array.isArray(paths) || !paths.every((p): p is string => typeof p === "string")) {
      throw new Error(`\`${shown}\` printed no \`${engineKind}\` list of paths: ${proc.stdout}`);
    }
    result[initKind].push(...paths);
  }
  return result;
}

/** Run `<engine> skills install --dir <root> --json` and map its paths to init kinds. */
function installSkills(engine: string, root: string): InitResult {
  return runEngineInstall(engine, ["skills", "install", "--dir", root], SKILLS_KINDS, "skills install");
}

/**
 * The command an MCP client runs to start the server: `pnpm exec attest mcp` in a pnpm
 * project (`pnpm-lock.yaml` in `root`), else `npx --no attest mcp`. `--no` makes a missing
 * local install fail instead of letting npx fetch the unscoped `attest` package.
 */
function mcpLaunchCommand(root: string): string[] {
  return fs.existsSync(path.join(root, "pnpm-lock.yaml"))
    ? ["pnpm", "exec", "attest", "mcp"]
    : ["npx", "--no", "attest", "mcp"];
}

/** Run `<engine> mcp install --dir <root> --command <launch> --json`. */
function installMcp(engine: string, root: string): InitResult {
  return runEngineInstall(
    engine,
    ["mcp", "install", "--dir", root, "--command", JSON.stringify(mcpLaunchCommand(root))],
    MCP_KINDS,
    "mcp install",
  );
}

function printSkills(root: string, skills: InitResult): void {
  const abs = (rel: string) => path.join(root, ...rel.split("/"));
  for (const rel of skills.created) process.stdout.write(`Created: ${abs(rel)}\n`);
  for (const rel of skills.updated) process.stdout.write(`Updated: ${abs(rel)}\n`);
  for (const rel of skills.skipped) process.stderr.write(`Skipped (unchanged): ${abs(rel)}\n`);
  for (const rel of skills.conflict) {
    process.stderr.write(
      `Conflict (a different attest entry, left as it is; ` +
        `\`attest-engine mcp install --force\` replaces it): ${abs(rel)}\n`,
    );
  }
}

async function runInit(parsed: InitArgs, root: string): Promise<InitResult | null> {
  const result = emptyInitResult();
  const installs: [boolean, string, (engine: string, root: string) => InitResult][] = [
    [parsed.skills, "skills", installSkills],
    [parsed.mcp, "mcp", installMcp],
  ];
  for (const [wanted, command, install] of installs) {
    if (!wanted) continue;
    const engine = await engineProviding(command);
    if (engine === null) return null;
    let installed: InitResult;
    try {
      installed = install(engine, root);
    } catch (err) {
      process.stderr.write(`attest: ${errorMessage(err)}\n`);
      return null;
    }
    if (!parsed.json) printSkills(root, installed);
    for (const kind of INIT_KINDS_WITH_CONFLICT) result[kind].push(...installed[kind]);
  }
  const scaffolded = scaffoldProject(root, {
    ...(parsed.ci ? { ci: "github" as const } : {}),
    quiet: parsed.json,
  });
  result.created.push(...scaffolded.created);
  result.skipped.push(...scaffolded.skipped);
  return result;
}

/**
 * Run `attest init [--ci github] [--skills] [--mcp] [--json]` in the current directory.
 *
 * `--skills` installs the agent skills with `attest-engine skills install` before
 * scaffolding; a missing or failing engine exits 1 with nothing scaffolded. `--json`
 * prints one `{created, updated, skipped}` document (plus `conflict` with `--mcp`) (paths relative to the current
 * directory, sorted) as the only stdout; everything else goes to stderr.
 */
async function cmdInit(args: string[]): Promise<number> {
  if (args.some((a) => HELP_FLAGS.has(a))) {
    process.stdout.write(`${INIT_USAGE}\n`);
    return 0;
  }
  const parsed = parseInitArgs(args);
  if (parsed === null) {
    process.stderr.write(`${INIT_USAGE}\n`);
    return 2;
  }
  const root = process.cwd();
  if (!parsed.json) return (await runInit(parsed, root)) === null ? 1 : 0;
  // Under --json, stdout carries the document only: anything else written goes to stderr.
  const stdoutWrite = process.stdout.write;
  process.stdout.write = process.stderr.write.bind(process.stderr) as typeof process.stdout.write;
  let result: InitResult | null;
  try {
    result = await runInit(parsed, root);
  } finally {
    process.stdout.write = stdoutWrite;
  }
  if (result === null) return 1;
  const done = result;
  const kinds = parsed.mcp ? INIT_KINDS_WITH_CONFLICT : INIT_KINDS;
  const sorted = Object.fromEntries(kinds.map((k) => [k, [...done[k]].sort()]));
  process.stdout.write(`${JSON.stringify(sorted)}\n`);
  return 0;
}

/** Dispatch `attest` arguments and resolve to the process exit code. */
export async function main(argv: string[] = process.argv.slice(2)): Promise<number> {
  const [command] = argv;
  if (command === undefined || HELP_FLAGS.has(command) || (command === "help" && argv.length === 1)) {
    return printHelp();
  }
  if (command === "--version") {
    process.stdout.write(`attest ${VERSION} (requires attest-engine ${ENGINE_VERSION})\n`);
    return 0;
  }
  if (command === "init") {
    return cmdInit(argv.slice(1));
  }
  if (command.startsWith("-")) {
    process.stderr.write(`attest: unknown option ${command}\n\n${SDK_USAGE}`);
    return 2;
  }
  return forward(argv);
}

// Run when executed directly, including through the node_modules/.bin/attest symlink.
function isDirectExecution(): boolean {
  const script = process.argv[1];
  if (script === undefined || !fs.existsSync(script)) return false;
  return fs.realpathSync(script) === fs.realpathSync(fileURLToPath(import.meta.url));
}

if (isDirectExecution()) {
  main().then(
    (code) => process.exit(code),
    (err: unknown) => {
      process.stderr.write(`attest: ${errorMessage(err)}\n`);
      process.exit(1);
    },
  );
}
