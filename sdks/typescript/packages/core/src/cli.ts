#!/usr/bin/env node
/**
 * The `attest` command line (`npx attest`).
 *
 * `attest init` runs in the SDK. Every other command (`doctor`, `baseline`, `policy`,
 * `cache`, `calibrate`, `version` and any command a later engine adds) runs in
 * `attest-engine`: the CLI resolves a version-checked engine with `resolveEngine` and
 * forwards the arguments, stdio and exit code unchanged. A missing or mismatched engine
 * exits 1 before anything runs, so a gate such as `attest policy evaluate` never exits 0
 * without evaluating.
 */

import { spawnSync } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { ENGINE_VERSION, VERSION } from "./version.js";
import { resolveEngine } from "./engine-manager.js";

const HELP_FLAGS = new Set(["-h", "--help", "help"]);

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

async function engineOrError(): Promise<string | null> {
  try {
    return await resolveEngine();
  } catch (err) {
    process.stderr.write(`attest: ${err instanceof Error ? err.message : String(err)}\n`);
    return null;
  }
}

function engineCommands(engine: string): string {
  const proc = spawnSync(engine, ["help"], {
    stdio: ["ignore", "pipe", "pipe"],
    encoding: "utf-8",
  });
  if (proc.error) throw proc.error;
  if (proc.status !== 0) {
    throw new Error(`${engine} help exited ${String(proc.status)}: ${proc.stderr}`);
  }
  const lines = proc.stdout.split("\n");
  const start = lines.indexOf("commands:");
  if (start === -1) {
    throw new Error(`${engine} help printed no commands section:\n${proc.stdout}`);
  }
  return lines.slice(start + 1).join("\n");
}

async function printHelp(): Promise<number> {
  process.stdout.write(`${SDK_USAGE}\n`);
  const engine = await engineOrError();
  if (engine === null) {
    process.stdout.write("engine commands: unavailable (see error above)\n");
    return 1;
  }
  process.stdout.write(`engine commands (attest-engine ${ENGINE_VERSION}, ${engine}):\n`);
  process.stdout.write(`${engineCommands(engine)}\n`);
  return 0;
}

async function forward(args: string[]): Promise<number> {
  const engine = await engineOrError();
  if (engine === null) return 1;
  const proc = spawnSync(engine, args, { stdio: "inherit" });
  if (proc.error) throw proc.error;
  return proc.status ?? 1;
}

/** Dispatch `attest` arguments and resolve to the process exit code. */
export async function main(argv: string[] = process.argv.slice(2)): Promise<number> {
  const [command] = argv;
  if (command === undefined || HELP_FLAGS.has(command)) return printHelp();
  if (command === "--version") {
    process.stdout.write(`attest ${VERSION} (requires attest-engine ${ENGINE_VERSION})\n`);
    return 0;
  }
  if (command === "init") {
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
const isDirectExecution =
  process.argv[1] !== undefined &&
  fs.realpathSync(process.argv[1]) === fs.realpathSync(fileURLToPath(import.meta.url));

if (isDirectExecution) {
  main().then(
    (code) => process.exit(code),
    (err: unknown) => {
      process.stderr.write(`attest: ${err instanceof Error ? err.message : String(err)}\n`);
      process.exit(1);
    },
  );
}
