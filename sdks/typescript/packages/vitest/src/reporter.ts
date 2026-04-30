import { renderDiagnostics, renderSummary } from "@attest-ai/core";

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
