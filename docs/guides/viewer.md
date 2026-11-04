# Failure viewer

`attest view` turns report-v2 files into one HTML page you open in a browser: the test list, the trace tree with the failing span highlighted, expected versus actual, replay or live provenance, cost, a baseline-versus-head diff and the mutants that survived. It is a local file, not a server. The page has no JavaScript, makes no network request and loads nothing from outside itself.

```bash
ATTEST_REPORT_TRACES=1 uv run pytest --attest-report=attest-report.json
uv run attest view attest-report.json
```

With Vitest, set the same variable and write the report with the reporter (`--reporter=@attest-ai/vitest/reporter --outputFile.attest=attest-report.json`), then run `npx attest view attest-report.json`.

The page is written to `attest-report.html` next to the first report. It opens in the default browser only when stdout is a terminal, `CI` is unset and `--no-open` is absent; a failure to start the opener prints the path and still exits 0.

```text
attest view <report.json...> [--baseline <report.json>...] [--mutation <mutate.json>] [--out FILE] [--no-open] [--json]
```

| Option | Meaning |
|---|---|
| `<report.json...>` | One or more report-v2 files; several merge like `attest report summary`. Flags and paths may come in any order. |
| `--baseline <report.json>` | Report-v2 file(s) of the baseline run (repeat to merge several). Adds the baseline-versus-head section. |
| `--mutation <mutate.json>` | Output of `attest mutate --json`. Adds the surviving mutants. |
| `--out FILE` | Output path. Default `attest-report.html` beside the first report. `--out` may not name an input file. |
| `--no-open` | Never start a browser. |
| `--json` | Print `{out, reports, tests, failures, traces, baseline, mutation, opened}` (schema `cli/view.schema.json`). |

Exit 0: the page was written. Exit 2: usage error, an unreadable or invalid input, or the page could not be written. The file is written through a temporary file and renamed, with mode 0644.

A screenshot of the page for the INC-1042 duplicate-refund regression, with the second `issue_refund` span highlighted and the baseline diff, is at [images/failure-viewer.png](images/failure-viewer.png); the baseline section is at [images/failure-viewer-baseline.png](images/failure-viewer-baseline.png).

## What the page shows

- **Summary.** Tests passed, failed, not evaluated (with the reasons) and skipped; the assertion pass rate with `not_evaluated` left out of the denominator ([verdicts](verdicts.md)); model calls replayed versus live; attest cost; duration.
- **Tests.** One row per test with its outcome and `file:line`. Every non-passing test opens by default.
- **Findings.** For each assertion that did not pass: its type, status and reason, the explanation, expected beside actual, the test's provenance (the replay mode and fixtures, or `live`), the cost, and the trace node the failure points at.
- **Trace tree.** The steps of each trace the test's rows were evaluated against, nested by sub-traces, each with its type, name and arguments and result. The step a failing row points at is outlined, opened and marked `◀ failing span`; the marker links to the assertion, and the assertion links back to the step.
- **Baseline versus head.** Assertion pass rate, failed and not-evaluated counts, cost, total latency and per-test latency p50 and p95, then every test whose outcome or tool sequence changed, with the added (`+`) and removed (`-`) calls.
- **Mutation testing.** Score, and each surviving mutant with the contract that would kill it ([mutation](mutation.md)).

## Traces in reports

Reports carry no traces unless the run sets `ATTEST_REPORT_TRACES=1`. Then the pytest plugin and the Vitest reporter add a top-level `traces` map (trace id to trace-v2 document, equal traces stored once) and a `trace_id` on each row evaluated against a trace. Both are optional report-v2 fields: the schema, `report_version` and every existing consumer are unchanged. Two different traces that share a `trace_id` are stored as `<id>`, `<id>#2`, and so on, so a row's `trace_id` always names exactly the trace it was evaluated against.

Without traces the page still renders, says how to enable them and leaves the trace tree and the tool-sequence diff empty.

> **Privacy.** A trace holds prompts, model outputs and tool arguments. A report written with `ATTEST_REPORT_TRACES=1` contains all of them, and so does the HTML page, so treat both as you treat the test inputs themselves. The variable is off by default. `attest init --ci github` turns it on for the test step and uploads `attest-report.json` and `attest-report.html` in the `attest-report` artifact; anyone who can download the repository's workflow artifacts can read them. Remove `ATTEST_REPORT_TRACES` from the workflow when traces must not leave the job.

### The failing span

A `tool_cardinality` check that fails on `max` sets the row's `trace_node_path` to `steps[N]`: the first call over the limit (for a `per` key, the first violating key's first call over the limit). `N` is the position of the call in the trace's top-level `steps`; steps inside sub-traces are not indexed. For the double refund of INC-1042 with `max: 1`, that is the second `issue_refund`. A `min` violation and every other check keep their inferred path (`trace.<check>`, `output.message`, ...).

The grammar the viewer resolves is `steps[N]`, optionally followed by `.field` (`steps[3].result`), where `N` is a decimal index. Any other path is shown as text and highlights no step.

## Baseline

The comparison is report to report, not against the baseline store of [baselines](baselines.md): pass `--baseline` the reports of a known-good run (in CI, the report artifact of the latest successful run on `main`). Tests match by test id. The tool sequence of a test is the tool-call names of its traces, so the sequence diff needs traces in both runs; without them the page says so and compares the other metrics.

## In CI

`attest init --ci github` adds `ATTEST_REPORT_TRACES: "1"` to the test step, a `Write the failure viewer` step (`attest view attest-report.json --no-open --out attest-report.html`, `if: always()`) and `attest-report.html` to the files of the `attest-report` artifact. Download the artifact from the workflow run and open the page. See the [merge gate guide](merge-gate.md).

## Security model

A report and its traces are untrusted: a trace holds whatever the agent, a tool or a user produced. The page is built so that none of it can run or fetch anything:

- One `html/template` file. Every report string is escaped for the context it lands in; the engine never converts report data to `template.HTML`, `template.JS`, `template.URL` or `template.CSS`.
- No JavaScript at all (the tree is `<details>`) and a `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">`, so a browser blocks scripts, images, frames and every request even if escaping failed.
- No `src` or `href` that points outside the page: links are `#anchors` whose ids come from indices, never from report text.
- Every rendered string is cut to 4 KiB with a marker, the page carries at most 8 MiB of report text, and trace trees are limited to 500 steps per trace, 3000 per page and depth 6 (the failing step is always kept). The page lists what was cut. Inputs over 256 MiB are refused.
- The opener is started with the page path as one argument, never through a shell, and only on a terminal outside CI.
