#!/usr/bin/env node
/**
 * CLI entry point for Attest: npx attest or attest (after global install).
 *
 * Ported from Python sdks/python/src/attest/__main__.py
 */

import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";
import { VERSION } from "./version.js";
import {
  computeAgreement,
  loadLabelsCSV,
  loadLabelsJSONL,
  type LabelPair,
} from "./calibration.js";

function cacheDir(): string {
  const envOverride = process.env["ATTEST_CACHE_DIR"];
  if (envOverride) return envOverride;
  return path.join(os.homedir(), ".attest", "cache");
}

function cacheDbPath(): string {
  return path.join(cacheDir(), "attest.db");
}

function cmdCacheStats(): void {
  const dbPath = cacheDbPath();
  const exists = fs.existsSync(dbPath);
  let fileSize = 0;
  if (exists) {
    fileSize = fs.statSync(dbPath).size;
  }
  const stats = { exists, file_size: fileSize, path: dbPath };
  process.stdout.write(JSON.stringify(stats) + "\n");
}

function cmdCacheClear(): void {
  const dbPath = cacheDbPath();
  if (fs.existsSync(dbPath)) {
    fs.unlinkSync(dbPath);
    process.stdout.write(`Cleared cache: ${dbPath}\n`);
  } else {
    process.stdout.write(`No cache to clear: ${dbPath}\n`);
  }
}

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

function cmdValidate(targetDir: string): void {
  const testsDir = path.join(targetDir, "tests");

  let testFiles: string[] = [];
  if (fs.existsSync(testsDir)) {
    testFiles = fs
      .readdirSync(testsDir)
      .filter((f) => f.endsWith(".test.ts") || f.endsWith(".test.js"));
  }

  if (testFiles.length === 0) {
    process.stderr.write(
      `Warning: no test files (*.test.ts / *.test.js) found in ${testsDir}.\n`,
    );
  } else {
    process.stdout.write(`Found ${testFiles.length} test file(s) in ${testsDir}\n`);
  }

  // Check for stale golden traces
  const STALE_DAYS = 30;
  const now = Date.now();
  const goldenFiles = findGoldenFiles(targetDir);
  for (const golden of goldenFiles) {
    const mtime = fs.statSync(golden).mtimeMs;
    const ageDays = Math.floor((now - mtime) / (1000 * 60 * 60 * 24));
    if (ageDays > STALE_DAYS) {
      process.stderr.write(
        `Warning: golden trace ${golden} is ${ageDays} days old (>${STALE_DAYS} days). Consider regenerating.\n`,
      );
    }
  }
}

function findGoldenFiles(dir: string): string[] {
  const results: string[] = [];
  if (!fs.existsSync(dir)) return results;

  const entries = fs.readdirSync(dir, { withFileTypes: true });
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory() && entry.name !== "node_modules") {
      results.push(...findGoldenFiles(fullPath));
    } else if (entry.isFile() && entry.name.endsWith(".golden")) {
      results.push(fullPath);
    }
  }
  return results;
}

export function main(argv: string[] = process.argv.slice(2)): void {
  const args = argv;

  if (args[0] === "--version") {
    process.stdout.write(`attest ${VERSION}\n`);
    process.exit(0);
  }

  if (args[0] === "cache" && args.length >= 2) {
    if (args[1] === "stats") {
      cmdCacheStats();
      process.exit(0);
    } else if (args[1] === "clear") {
      cmdCacheClear();
      process.exit(0);
    } else {
      process.stderr.write(`Unknown cache subcommand: ${args[1]}\n`);
      process.stderr.write("Available: cache stats, cache clear\n");
      process.exit(1);
    }
  }

  if (args[0] === "init") {
    cmdInit(process.cwd());
    process.exit(0);
  }

  if (args[0] === "validate") {
    cmdValidate(process.cwd());
    process.exit(0);
  }

  if (args[0] === "calibrate") {
    cmdCalibrate(args.slice(1));
    process.exit(0);
  }

  process.stderr.write(
    "Usage: attest <command>\n\n" +
    "Commands:\n" +
    "  --version       Print version\n" +
    "  init            Scaffold a test project\n" +
    "  validate        Validate test suite\n" +
    "  cache stats     Show cache statistics\n" +
    "  cache clear     Clear cache database\n" +
    "  calibrate       Compute judge agreement metrics over labeled data\n",
  );
  process.exit(args.length === 0 ? 0 : 1);
}

interface CalibrateOptions {
  labels: string;
  rubric: string;
  rubricVersion: string;
  threshold: number;
}

function parseCalibrateArgs(args: string[]): CalibrateOptions {
  const opts: CalibrateOptions = {
    labels: "",
    rubric: "default",
    rubricVersion: "",
    threshold: 0.5,
  };
  for (let i = 0; i < args.length; i++) {
    const arg = args[i]!;
    const next = args[i + 1];
    if (arg === "--labels") {
      if (next === undefined) throw new Error("--labels requires a path");
      opts.labels = next;
      i++;
    } else if (arg === "--rubric") {
      if (next === undefined) throw new Error("--rubric requires a name");
      opts.rubric = next;
      i++;
    } else if (arg === "--rubric-version") {
      if (next === undefined) throw new Error("--rubric-version requires a value");
      opts.rubricVersion = next;
      i++;
    } else if (arg === "--threshold") {
      if (next === undefined) throw new Error("--threshold requires a number");
      const parsed = Number(next);
      if (next.trim() === "" || Number.isNaN(parsed)) {
        throw new Error(`--threshold value ${JSON.stringify(next)} is not a number`);
      }
      opts.threshold = parsed;
      i++;
    } else {
      throw new Error(`unknown calibrate flag: ${arg}`);
    }
  }
  if (!opts.labels) {
    throw new Error("--labels is required");
  }
  return opts;
}

function cmdCalibrate(args: string[]): void {
  let opts: CalibrateOptions;
  try {
    opts = parseCalibrateArgs(args);
  } catch (err) {
    process.stderr.write(`calibrate: ${(err as Error).message}\n`);
    process.exit(2);
    return;
  }

  const text = fs.readFileSync(opts.labels, "utf-8");
  const ext = path.extname(opts.labels).toLowerCase();
  let records;
  try {
    if (ext === ".jsonl" || ext === ".ndjson") {
      records = loadLabelsJSONL(text);
    } else if (ext === ".csv") {
      records = loadLabelsCSV(text);
    } else if (text.trimStart().startsWith("{")) {
      records = loadLabelsJSONL(text);
    } else {
      records = loadLabelsCSV(text);
    }
  } catch (err) {
    process.stderr.write(`calibrate: ${(err as Error).message}\n`);
    process.exit(1);
    return;
  }

  const pairs: LabelPair[] = records
    .filter((r) => r.judgeKnown)
    .map((r) => ({ human: r.humanLabel, judge: r.judgeScore }));
  const missing = records.filter((r) => !r.judgeKnown).length;
  if (pairs.length === 0) {
    process.stderr.write(
      `calibrate: no rows had judge_score (${missing} rows missing); pre-score the labels file first\n`,
    );
    process.exit(1);
    return;
  }

  let result;
  try {
    result = computeAgreement(pairs, opts.threshold);
  } catch (err) {
    process.stderr.write(`calibrate: ${(err as Error).message}\n`);
    process.exit(1);
    return;
  }

  const output = {
    rubric_name: opts.rubric,
    rubric_version: opts.rubricVersion,
    threshold: result.threshold,
    label_count: result.n,
    agreement: round3(result.agreement),
    cohen_kappa: round3(result.cohenKappa),
    roc_auc: round3(result.rocAuc),
    missing_judge: missing,
  };
  process.stdout.write(JSON.stringify(output, null, 2) + "\n");
}

function round3(x: number): number {
  return Math.round(x * 1000) / 1000;
}

// Run when executed directly
const isDirectExecution =
  typeof import.meta.url === "string" &&
  import.meta.url.startsWith("file:") &&
  process.argv[1] !== undefined &&
  import.meta.url.endsWith(path.basename(process.argv[1]));

if (isDirectExecution) {
  main();
}
