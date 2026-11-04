/**
 * Scaffolding for `attest init`.
 *
 * `attest init` writes a sample Vitest test that runs with no model, API key or
 * network: the agent run is a trace built by hand with `TraceBuilder` and every
 * assertion is deterministic (layers 1-4). `attest init --ci github` also writes a
 * GitHub Actions workflow that gates merges on the test run, the report and the
 * policy.
 */
import * as fs from "node:fs";
import * as path from "node:path";
import { VERSION } from "./version.js";

/** Marks the line a new user edits to see a failure. */
export const DEMO_MARKER = "ATTEST-DEMO";

export const CI_PROVIDERS = ["github"] as const;
export type CiProvider = (typeof CI_PROVIDERS)[number];

export const SAMPLE_TEST_TEMPLATE = `// Sample Attest test written by \`attest init\`. Run it with \`npx vitest run\`.
import { it } from "vitest";
import { attestExpect, TraceBuilder, type Trace } from "@attest-ai/core";
import { evaluate } from "@attest-ai/vitest";

// The trace of one agent run, built by hand so the sample needs no model, API key
// or network. Replace it with a trace from your agent: wrap the agent with
// \`new Agent(...)\` or record it with an adapter such as OpenAIAdapter.
function myAgentResult(): Trace {
  return new TraceBuilder("refund-agent")
    .setInput({ user_message: "Please refund order ORD-1001." })
    .addToolCall("lookup_order", {
      args: { order_id: "ORD-1001" },
      result: { status: "delivered", amount: 42.0 },
    })
    .addToolCall("issue_refund", {
      args: { order_id: "ORD-1001", amount: 42.0 },
      result: { refund_id: "RF-1001" },
    })
    .setOutput({
      message: "Your refund of $42.00 has been issued.",
      structured: { refund_id: "RF-1001", amount: 42.0 },
    })
    .setMetadata({ total_tokens: 180, cost_usd: 0.002, latency_ms: 900 })
    .build();
}

it("my agent issues the refund", async (context) => {
  const chain = attestExpect(myAgentResult())
    .outputMatchesSchema({ type: "object", required: ["refund_id"] }) // layer 1: schema
    .costUnder(0.01) // layer 2: constraint
    .toolsCalledInOrder(["lookup_order", "issue_refund"]) // layer 3: trace
    .outputContains("refund"); // ${DEMO_MARKER}: change "refund" to "store credit"
  await evaluate(chain, { context });
});
`;

/** Vitest arguments that add AttestReporter and write the report-v2 file. */
export const REPORT_ARGS =
  "--reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=attest-report.json";

export const POLICY_TEMPLATE = `# Merge policy for \`attest policy evaluate --report <report> --policy attest.policy.yaml\`.
# Exit 0: pass. Exit 1: a blocking rule failed. Exit 2: warnings only.
layers:
  0:  # layer 0 caps every layer together
    max_hard_fails: 0
# Assertions that could not run (no judge configured, simulation) warn instead of block.
not_evaluated: warn
`;

export const GITHUB_WORKFLOW_TEMPLATE = `# Written by \`attest init --ci github\`.
# The test step records its outcome and continues so the report is always uploaded,
# the JUnit file written, the policy evaluated and the pull request summarised; the last
# step fails the job when the tests failed. A policy warning (exit 2) does not fail the job.
name: attest

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  attest:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: write
    steps:
      - uses: actions/checkout@v6
        with:
          persist-credentials: false
      - uses: actions/setup-node@v6
        with:
          node-version: 22
      - name: Install dependencies
        run: npm ci
      - name: Run tests
        id: tests
        continue-on-error: true
        env:
          ATTEST_REPORT_TRACES: "1"
        run: npx vitest run ${REPORT_ARGS}
      - name: Write the JUnit report
        if: always()
        run: npx attest report junit --report attest-report.json --out attest-junit.xml
      - name: Write the failure viewer
        if: always()
        run: npx attest view attest-report.json --no-open --out attest-report.html
      - name: Upload the Attest report
        if: always()
        uses: actions/upload-artifact@v7
        with:
          name: attest-report
          path: |
            attest-report.json
            attest-junit.xml
            attest-report.html
          if-no-files-found: error
      - name: Evaluate the merge policy
        if: always()
        run: |
          set +e
          npx attest policy evaluate --report attest-report.json --policy attest.policy.yaml
          code=$?
          if [ "$code" -eq 2 ]; then
            echo "::warning::attest policy: warnings only (exit 2)"
            exit 0
          fi
          exit "$code"
      - name: Annotate the pull request
        # Posts one summary comment (updated on every run) and annotates failures at their
        # file and line. Fork pull requests get a read-only token that cannot comment; the
        # last step still fails the job when tests failed.
        if: >-
          always() && github.event_name == 'pull_request' &&
          github.event.pull_request.head.repo.full_name == github.repository
        uses: attest-framework/attest/.github/actions/pr-annotate@v${VERSION}
        with:
          report: attest-report.json
      - name: Fail the job when tests failed
        if: always() && steps.tests.outcome == 'failure'
        run: |
          echo "::error::vitest failed; see the Run tests step"
          exit 1
`;

/**
 * Files `scaffoldProject` wrote (`created`) and left alone because they existed
 * (`skipped`), as forward-slash paths relative to the target directory.
 */
export interface ScaffoldResult {
  created: string[];
  skipped: string[];
}

function write(
  targetDir: string,
  rel: string,
  content: string,
  result: ScaffoldResult,
  quiet: boolean,
): void {
  const file = path.join(targetDir, ...rel.split("/"));
  if (fs.existsSync(file)) {
    result.skipped.push(rel);
    if (!quiet) process.stderr.write(`Skipped (exists): ${file}\n`);
    return;
  }
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, content);
  result.created.push(rel);
  if (!quiet) process.stdout.write(`Created: ${file}\n`);
}

/**
 * Write the sample test and the merge policy into `targetDir`; `ci: "github"` also
 * writes `.github/workflows/attest.yml`. Existing files are never overwritten. No
 * Vitest config is written: the workflow adds AttestReporter on the command line
 * (`REPORT_ARGS`), so an existing config keeps working unchanged. `quiet` suppresses the
 * per-file lines and the next-step hint. Returns what was written.
 */
export function scaffoldProject(
  targetDir: string,
  options: { ci?: CiProvider; quiet?: boolean } = {},
): ScaffoldResult {
  const quiet = options.quiet ?? false;
  const result: ScaffoldResult = { created: [], skipped: [] };
  write(targetDir, "tests/my-agent.test.ts", SAMPLE_TEST_TEMPLATE, result, quiet);
  write(targetDir, "attest.policy.yaml", POLICY_TEMPLATE, result, quiet);
  if (options.ci === "github") {
    write(targetDir, ".github/workflows/attest.yml", GITHUB_WORKFLOW_TEMPLATE, result, quiet);
  }
  if (!quiet) {
    process.stdout.write(
      `\nNext: run \`npx vitest run\`. Then change the line marked ${DEMO_MARKER} in ` +
        "tests/my-agent.test.ts and run it again to see a failure.\n",
    );
  }
  return result;
}
