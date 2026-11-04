# Set up the attest merge gate in CI

Goal: a GitHub Actions job that runs the attest tests with zero model calls, writes a report, evaluates a policy, annotates the pull request and fails on regressions. Background: `attest guide merge-gate`, `attest guide policies`, `attest guide annotations`, `attest guide selection`.

## 1. Scaffold

```bash
uv run attest init --ci github --json      # Python
npx attest init --ci github --json         # TypeScript
```

`--json` prints `{created, updated, skipped}` (paths relative to the project root). `init` never overwrites an existing file; a skipped workflow or policy means the repo already has one: read it instead of replacing it. Add `--skills` to also install the attest agent skills. Files: `.github/workflows/attest.yml`, `attest.policy.yaml`, and a sample test if none exists (delete the sample once a real test exists, with the user's agreement).

The generated job, in order: install (`uv sync --locked` / `npm ci`); **Run tests** with `continue-on-error: true` and the report flag (`uv run pytest --attest-report=attest-report.json`, or Vitest with `--reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=attest-report.json`); JUnit (`attest report junit --report attest-report.json --out attest-junit.xml`); the failure viewer (`attest view attest-report.json --no-open --out attest-report.html`; the test step sets `ATTEST_REPORT_TRACES: "1"`, which embeds prompts, outputs and tool arguments in the report and the page); artifact upload (report, JUnit and HTML); **Evaluate the merge policy** (exit 2 is a warning, any other non-zero fails); **Annotate the pull request** with `attest-framework/attest/.github/actions/pr-annotate@v<sdk version>`; **Fail the job when tests failed**. Steps after the tests run with `if: always()`.

## 2. Make the job model-free

- Replay is `strict` when `CI` is set, so a changed prompt fails with the miss instead of calling a model. Add the egress guard to the test step: pytest `--attest-replay=strict --attest-egress=block --attest-strict`; Vitest env `ATTEST_REPLAY: strict`, `ATTEST_EGRESS: block`, `ATTEST_STRICT: "1"`.
- Do not add provider API keys as workflow secrets for the test job. Give the agent placeholder values for the settings it requires (`OPENAI_API_KEY: replay-needs-no-key`) as job `env`.
- Judge or embedding assertions then report `not_evaluated: provider_not_configured`. Keep them visible: the policy's `not_evaluated: warn` (exit 2) reports them without blocking. Use `not_evaluated: block` only when every assertion in CI is deterministic.
- Never edit the workflow to ignore exit 1 from tests or the policy.

## 3. The policy

`attest.policy.yaml` (generated): any hard failure blocks, `not_evaluated` warns. Read `attest guide policies` for `max_cost_usd`, per-layer caps, `failure_classes` and baseline regression rules. Check it locally against a real report:

```bash
uv run pytest --attest-report=attest-report.json
uv run attest policy evaluate --policy attest.policy.yaml --report attest-report.json; echo "exit $?"
```

| Exit | Meaning | In the workflow |
| --- | --- | --- |
| 0 | pass | pass |
| 1 | a `block` rule was violated | fail |
| 2 | warnings only | warning annotation, pass |
| 3 | usage error, or the policy or report could not be loaded | fail; fix the path or the YAML |

## 4. Pull request annotations

`pr-annotate` posts one summary comment (updated on each run) and an annotation per failure and replay miss at its file and line. It needs `pull-requests: write` on the job and runs only for pull requests from the same repository (fork pull requests get the summary in the job summary only). Keep the action pinned to the release tag `init` wrote; with a GitHub App token set `comment-author: <app-slug>[bot]`. Preview the comment locally:

```bash
uv run attest policy evaluate --policy attest.policy.yaml --report attest-report.json > policy-result.json
uv run attest report summary --report attest-report.json --policy-result policy-result.json
```

## 5. Run only affected tests (optional)

For slow suites, select tests from the diff. It needs the base ref (`actions/checkout` with `fetch-depth: 0`):

```bash
tests=$(uv run attest select --base origin/main)
if [ "$tests" = "all" ]; then uv run pytest --attest-report=attest-report.json
elif [ -n "$tests" ]; then uv run pytest $tests --attest-report=attest-report.json
else echo "no tests selected"; fi
```

`attest select --base origin/main --json` prints `{mode, tests, reasons}`. A source, lockfile, policy or world change selects `all`; a fixture change selects the tests that name it (example: `attest_replay("checkout-timeout")`). Keep a full run on pushes to `main`.

## 6. Verify

Run the workflow's commands locally in order (tests with the report, junit, policy) and report each exit code. Commit only the files `init` created plus your edits; do not push or open a pull request unless the user asks.
