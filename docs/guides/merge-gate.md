# From first test to merge gate

This guide takes a project from no tests to a GitHub Actions job that blocks merges on agent regressions. No API key, model or network access is needed: the sample test checks a recorded agent run with deterministic assertions (layers 1–4).

## 1. Scaffold a passing test

Python:

```bash
uv add "attest-ai>=1.0.0rc1" pytest
uv run attest init
uv run pytest            # 1 passed
```

TypeScript:

```bash
npm install -D "@attest-ai/core@>=1.0.0-rc.1" "@attest-ai/vitest@>=1.0.0-rc.1" "vitest@^4"
npx attest init
npx vitest run          # 1 passed
```

`attest init` never overwrites a file that exists. It writes:

| File | Python | TypeScript |
|---|---|---|
| Sample test | `tests/test_my_agent.py` | `tests/my-agent.test.ts` |
| Agent run | the `my_agent_result` fixture in the test file, a trace built with `TraceBuilder` | `myAgentResult()` in the test file |
| Merge policy | `attest.policy.yaml` | `attest.policy.yaml` |

Replace the hand-built trace with one from your agent: wrap it with `@attest.agent` / `new Agent(...)`, or record it with a provider adapter.

## 2. See a failure

Change `"refund"` to `"store credit"` on the line marked `ATTEST-DEMO` and run the tests again. The test fails (exit code 1) and the diagnostic names the layer, the expectation and what the agent produced:

```text
Attest diagnostic — 1 of 4 assertions failed:
  FAIL assert_2d1d647b — L4 content
      trace path: output.message
      expected:   contains "store credit"
      actual:     Your refund of $42.00 has been issued.
```

## 3. Write the report

| Runner | Command |
|---|---|
| pytest | `uv run pytest --attest-report=attest-report.json` |
| Vitest | `npx vitest run --reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=attest-report.json`, or `reporters: ["default", new AttestReporter({ outputFile: "attest-report.json" })]` in the Vitest config |

The report follows `proto/attest/v1/report-v2.schema.json`:

- `results`: one row per assertion, with the engine's status (`pass`, `soft_fail`, `hard_fail`, `not_evaluated` + `reason`), layer, expected and actual, plus the `test_id`, `file` and `line` of the test that requested it.
- `tests`: one row per test, including tests without Attest assertions, with `outcome` (`passed`, `failed`, `skipped`), `file`, `line` and the reasons of any not-evaluated assertions.
- `summary`: assertion counts; `not_evaluated` is counted separately.

`file` is relative to the test runner's root directory (pytest `rootdir`, Vitest `root`) with forward slashes; `line` is the line of the test definition (`def test_…` / `it(…)`). The pytest process exits with the test result as usual; writing the report never changes it.

## 4. Evaluate the policy

```bash
uv run attest policy evaluate --report attest-report.json --policy attest.policy.yaml
```

| Exit | Meaning |
|---|---|
| 0 | Every rule passed |
| 1 | A blocking rule failed — the generated policy blocks on any hard failure |
| 2 | Warnings only — the generated policy warns when assertions were not evaluated (`not_evaluated: warn`) |

## 5. Gate merges in GitHub Actions

`attest init --ci github` writes `.github/workflows/attest.yml`:

1. **Run tests** has `continue-on-error: true` and `id: tests`, so a failing test does not stop the job before the report is handled.
2. **Write the JUnit report** runs `attest report junit --report attest-report.json --out attest-junit.xml`. **Write the failure viewer** runs `attest view attest-report.json --no-open --out attest-report.html`. **Upload the Attest report** uploads the report, the JUnit file and the HTML page as the `attest-report` artifact. The test step sets `ATTEST_REPORT_TRACES: "1"` so the page can show the trace tree; see [Failure viewer](viewer.md) for what that puts in the artifact.
3. **Evaluate the merge policy** treats exit 2 as a warning and fails on exit 1.
4. **Annotate the pull request** uses the `pr-annotate` action at the SDK's release tag. It posts one summary comment and emits a workflow annotation for each failure and replay miss; see [Annotations and the summary comment](annotations.md). It runs only for pull requests from branches of the same repository; on a fork's pull request the token is read-only, so the action posts no comment and the summary is in the job summary (annotations still appear).
5. **Fail the job when tests failed** checks `steps.tests.outcome == 'failure'` and exits 1, so a red test is never hidden by `continue-on-error`.

Steps 2 to 4, and the viewer step, run with `if: always()`.

Permissions are minimal. The workflow grants `contents: read`, and the job adds `pull-requests: write`, which the summary comment needs.

`pr-annotate` also evaluates a policy itself when you pass `policy:`. Its `exit-code` output is the evaluator's exit code; the step fails on 1 (unless `fail-on-violation: false`) and on any code other than 0, 1 and 2. The workflow above evaluates the policy in its own step instead, so the action runs without `policy:`.
