import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, relative, resolve, sep } from "node:path";
import { isSimulationMode, renderDiagnostics, renderSummary } from "@attest-ai/core";
import type { Reporter, TestModule, Vitest } from "vitest/node";
import { buildReport, type TestRecord } from "./report.js";

interface ReporterContext {
  printInfo: (message: string) => void;
}

/**
 * Vitest reporter that prints the cost report and a hierarchical
 * diagnostic block for every failed Attest evaluation in the run.
 *
 * Produces the same shape as the Python pytest plugin's
 * "Attest Diagnostics" section so reviewers see consistent output
 * across both ecosystems.
 */
export class AttestCostReporter {
  onFinished(_files: unknown, _errors: unknown, context?: ReporterContext): void {
    const cost = globalThis.__attest_session_cost__ ?? 0;
    const softFailures = globalThis.__attest_session_soft_failures__ ?? 0;
    const failures = globalThis.__attest_session_failures__ ?? [];

    const lines: string[] = [];

    if (failures.length > 0) {
      lines.push(
        "",
        "=".repeat(50),
        "  Attest Diagnostics",
        "=".repeat(50),
      );
      for (const result of failures) {
        lines.push(renderDiagnostics(result));
        lines.push(`  Summary: ${renderSummary(result)}`);
      }
    }

    lines.push(
      "",
      "=".repeat(50),
      "  Attest Cost Report",
      "=".repeat(50),
      `  Total LLM cost this session: $${cost.toFixed(6)} USD`,
      `  Soft failures recorded:       ${softFailures}`,
      `  Failed runs:                  ${failures.length}`,
      "=".repeat(50),
      "",
    );

    const output = lines.join("\n");

    if (context?.printInfo) {
      context.printInfo(output);
    } else {
      process.stdout.write(output);
    }
  }
}

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
 */
export class AttestReporter implements Reporter {
  private outputFile: string | undefined;
  private root = process.cwd();

  constructor(options: AttestReporterOptions = {}) {
    this.outputFile = options.outputFile;
  }

  onInit(vitest: Vitest): void {
    this.root = vitest.config.root;
    const configured = vitest.config.outputFile;
    if (this.outputFile === undefined && typeof configured === "object") {
      this.outputFile = configured["attest"];
    }
    vitest.config.includeTaskLocation = true;
  }

  onTestRunEnd(testModules: ReadonlyArray<TestModule>): void {
    const outputFile = this.outputFile;
    if (outputFile === undefined) return;
    const tests: TestRecord[] = [];
    for (const testModule of testModules) {
      const file = relative(this.root, testModule.moduleId).split(sep).join("/");
      for (const testCase of testModule.children.allTests()) {
        const location = testCase.location;
        if (location === undefined) {
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
          line: location.line,
          outcome: state === "passed" || state === "failed" ? state : "skipped",
          durationMs: testCase.diagnostic()?.duration ?? 0,
          attest: testCase.meta().attest,
        });
      }
    }
    const path = resolve(this.root, outputFile);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, `${JSON.stringify(buildReport(tests, isSimulationMode()), null, 2)}\n`);
  }
}

/** `vitest run --reporter=@attest-ai/vitest/reporter` loads the default export. */
export default AttestReporter;
