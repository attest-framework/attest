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
 */

import { spawnSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { ENGINE_VERSION, VERSION } from "./version.js";
import { resolveEngine } from "./engine-manager.js";

const HELP_FLAGS = new Set(["-h", "--help"]);

const SDK_USAGE =
  "usage: attest <command> [args]\n" +
  "\n" +
  "SDK commands:\n" +
  "  init  scaffold an attest test project in the current directory\n" +
  "\n" +
  "options:\n" +
  "  --version  print the SDK version and the engine version it requires\n" +
  "  --help     print this help\n";

const CONFTEST_TEMPLATE = `import { describe, it, expect } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";

describe("my agent", () => {
  it("returns expected output", async () => {
    const trace = new TraceBuilder("my-agent")
      .setInput({ user_message: "hello" })
      .setOutput({ message: "world" })
      .build();

    attestExpect(trace).outputContains("world");
  });
});
`;

function cmdInit(targetDir: string): void {
  const testsDir = path.join(targetDir, "tests");
  fs.mkdirSync(testsDir, { recursive: true });
  process.stdout.write(`Directory: ${testsDir}\n`);

  const sampleTestPath = path.join(testsDir, "test_my_agent.test.ts");
  if (fs.existsSync(sampleTestPath)) {
    process.stderr.write(`Skipped (exists): ${sampleTestPath}\n`);
  } else {
    fs.writeFileSync(sampleTestPath, CONFTEST_TEMPLATE);
    process.stdout.write(`Created: ${sampleTestPath}\n`);
  }
}

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
    const firstLine = errorMessage(err).split("\n")[0] ?? "";
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

async function forward(args: string[]): Promise<number> {
  let engine: string;
  try {
    engine = await resolveEngine();
  } catch (err) {
    process.stderr.write(`attest: ${errorMessage(err)}\n`);
    return 1;
  }
  const command = args[0] ?? "";
  const commands = engineCommands(engine);
  if (commands === null || (command !== "help" && !commands.has(command))) {
    process.stderr.write(
      `attest: attest-engine at ${engine} (version ${ENGINE_VERSION}) predates ` +
        `\`attest ${command}\`: its help does not list that command. Set ATTEST_ENGINE_PATH ` +
        "to an engine built from this release (make engine), or install a release whose " +
        "engine provides it.\n",
    );
    return 1;
  }
  const proc = spawnSync(engine, args, { stdio: "inherit" });
  if (proc.error) throw proc.error;
  return exitStatus(proc.status, proc.signal);
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
    if (argv.length > 1) {
      process.stderr.write("usage: attest init  (scaffolds into the current directory)\n");
      return HELP_FLAGS.has(argv[1] ?? "") ? 0 : 2;
    }
    cmdInit(process.cwd());
    return 0;
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
