import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, relative, resolve, sep } from "node:path";
import {
  isSimulationMode,
  parseUsage,
  sumUsage,
  type Usage,
} from "@attest-ai/core";
import type { Reporter, TestModule, Vitest } from "vitest/node";
import { buildReport, reportTracesEnabled, summaryLines, type TestRecord } from "./report.js";
import { BUDGET_MARKER_EXT, ROOT_ENV, USAGE_DIR_ENV } from "./process-env.js";

export interface AttestReporterOptions {
  /**
   * Path of the report-v2 JSON file, relative to the Vitest root. Overrides
   * `--outputFile.attest=<path>`.
   */
  outputFile?: string;
}

/**
 * Vitest reporter that writes a report-v2 file (proto/attest/v1/report-v2.schema.json)
 * when the run ends: every test with its outcome, file and line, and every Attest
 * assertion result tagged with the test that requested it. Feed the file to
 * `attest policy evaluate --report` and the pr-annotate action.
 *
 * Writes only when an output file is set: `new AttestReporter({ outputFile })` in the
 * Vitest config, or on the command line, the counterpart of pytest's
 * `--attest-report=<path>`:
 * `vitest run --reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=<path>`. Turns on `includeTaskLocation` so each test carries its
 * line. Requires Vitest 3+.
 *
 * At the end of the run it prints, with or without an output file, one line per
 * `trials()` test ("<test_id>: <passed>/<evaluated> trials passed (<n> not evaluated)")
 * and the "attest usage: …" line when the run made or refused an attest model call.
 */
export class AttestReporter implements Reporter {
  private outputFile: string | undefined;
  private root = process.cwd();
  private vitest: Vitest | undefined;
  private usageDir: string | undefined;

  constructor(options: AttestReporterOptions = {}) {
    this.outputFile = options.outputFile;
  }

  onInit(vitest: Vitest): void {
    this.vitest = vitest;
    this.root = vitest.config.root;
    const configured = vitest.config.outputFile;
    if (this.outputFile === undefined && typeof configured === "object") {
      this.outputFile = configured["attest"];
    }
    vitest.config.includeTaskLocation = true;
    // Workers inherit these: test ids hash paths relative to this root, and
    // evaluations outside any test drop their usage in usageDir.
    this.usageDir = mkdtempSync(join(tmpdir(), "attest-usage-"));
    process.env[ROOT_ENV] = this.root;
    process.env[USAGE_DIR_ENV] = this.usageDir;
  }

  onTestRunStart(): void {
    if (this.usageDir !== undefined) mkdirSync(this.usageDir, { recursive: true });
  }

  /**
   * Sum and remove the usage files workers wrote for evaluations outside any test, and note
   * whether any such evaluation returned `budget_exceeded`.
   */
  private takeUnattached(): { usage: Usage; budgetExceeded: boolean } {
    const dir = this.usageDir;
    if (dir === undefined) return { usage: sumUsage([]), budgetExceeded: false };
    const names = readdirSync(dir);
    const files = names.filter((f) => f.endsWith(".json"));
    const usage = sumUsage(files.map((f) => parseUsage(JSON.parse(readFileSync(join(dir, f), "utf8")))));
    const budgetExceeded = names.some((f) => f.endsWith(BUDGET_MARKER_EXT));
    rmSync(dir, { recursive: true, force: true });
    return { usage, budgetExceeded };
  }

  onTestRunEnd(testModules: ReadonlyArray<TestModule>): void {
    const outputFile = this.outputFile;
    const tests: TestRecord[] = [];
    for (const testModule of testModules) {
      const file = relative(this.root, testModule.moduleId).split(sep).join("/");
      for (const testCase of testModule.children.allTests()) {
        const location = testCase.location;
        // The location matters only for the report file; summary lines need none.
        if (location === undefined && outputFile !== undefined) {
          throw new Error(
            `AttestReporter: no source location for "${testCase.fullName}". Vitest records ` +
              "locations only when test.includeTaskLocation is true and the test file path matches " +
              "the root; set test.root to the real path (no symlinks) if it is set.",
          );
        }
        const state = testCase.result().state;
        tests.push({
          testId: `${file} > ${testCase.fullName}`,
          name: testCase.name,
          file,
          line: location?.line ?? 0,
          outcome: state === "passed" || state === "failed" ? state : "skipped",
          durationMs: testCase.diagnostic()?.duration ?? 0,
          attest: testCase.meta().attest,
          replay: testCase.meta().attestReplay,
        });
      }
    }
    const { usage: unattached, budgetExceeded: unattachedBudgetExceeded } = this.takeUnattached();
    const lines = summaryLines(tests, unattached);
    if (lines.length > 0) this.vitest?.logger.log(lines.join("\n"));
    if (outputFile === undefined) return;
    const path = resolve(this.root, outputFile);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, `${JSON.stringify(buildReport(tests, isSimulationMode(), unattached, reportTracesEnabled(), unattachedBudgetExceeded), null, 2)}\n`);
  }
}

/** `vitest run --reporter=@attest-ai/vitest/reporter` loads the default export. */
export default AttestReporter;
