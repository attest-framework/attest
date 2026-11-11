# From first test to merge gate

This guide takes a project from no tests to a GitHub Actions job that blocks merges on agent regressions. After installing the packages and engine, the sample test needs no API key, model or network access: it checks a hand-built agent trace with deterministic assertions (layers 1–4).

## 1. Scaffold a passing test

Python:

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
uv add "attest-ai>=1.1.0" pytest
uv run attest init
uv run pytest            # 1 passed
```

TypeScript:

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
npm install -D "@attest-ai/core@>=1.1.0" "@attest-ai/vitest@>=1.1.0" "vitest@^4"
npx attest init
npx attest doctor       # download/check the engine before the first test
npx vitest run          # 1 passed
```

On a cold `HOME`, the first engine download can exceed Vitest's default 5-second test timeout. Run `npx attest doctor` once before the first test run, or use the global setup in step 3.

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
| Vitest | `npx vitest run --reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=attest-report.json`, or `reporters: ["default", new AttestReporter({ outputFile: "attest-report.json" })]` in the Vitest config (import `AttestReporter` from `@attest-ai/vitest/reporter`) |

The Vitest config runs in the main process: import from the `@attest-ai/vitest/reporter` and `@attest-ai/vitest/setup` subpaths there, never from the package root. The root also exports the test helpers, which need the Vitest test runtime, and importing it from `vitest.config.ts` fails on Vitest 3. The two subpaths load on Vitest 3, 4 and 5 (the peer range is `>=3.0.0 <6`). `globalSetup: ["@attest-ai/vitest/setup"]` resolves the engine, downloading it once, before the first test, so the download neither slows nor times out a test.

```ts
import AttestReporter from "@attest-ai/vitest/reporter";
import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globalSetup: ["@attest-ai/vitest/setup"],
    reporters: ["default", new AttestReporter({ outputFile: "attest-report.json" })],
  },
});
```

The reporter collects the cost per test and writes it to the report-v2 file (`total_cost`, `summary.usage`) and prints the `attest usage:` line when the run made or refused an Attest model call; `AttestCostReporter` no longer exists, so remove it from the `reporters` list.

The report follows `proto/attest/v1/report-v2.schema.json`:

- `results`: one row per assertion, with the engine's status (`pass`, `soft_fail`, `hard_fail`, `not_evaluated` + `reason`), layer, expected and actual, plus the `test_id`, `file` and `line` of the test that requested it.
- `tests`: one row per test, including tests without Attest assertions, with `outcome` (`passed`, `failed`, `skipped`), `file`, `line` and the reasons of any not-evaluated assertions.
- `summary`: assertion counts; `not_evaluated` is counted separately.
- `tests_collected`: tests collected after deselection. Under pytest-xdist the controller writes one report that merges every worker's rows.

`file` is relative to the test runner's root directory (pytest `rootdir`, Vitest `root`) with forward slashes; `line` is the line of the test definition (`def test_…` / `it(…)`). The pytest process exits with the test result as usual; writing the report never changes it.

## 4. Evaluate the policy

<!-- snippet: setup=project -->
```bash
uv run attest policy evaluate --report attest-report.json --policy attest.policy.yaml
```

| Exit | Meaning |
|---|---|
| 0 | Every rule passed |
| 1 | A blocking rule failed — the generated policy blocks on any hard failure and on failed test outcomes (`tests_failed: block` by default) |
| 2 | Warnings only — the generated policy warns when assertions were not evaluated (`not_evaluated: warn`) |
| 3 | The policy could not be evaluated: a load error, a rule without its input (`block_on_regression` or `regression` without `--baseline`, `min_mutation_score` without `--mutation`), a report with 0 tests while `tests_collected` is above 0, or a baseline pinned under id scheme 1 (`baseline uses id scheme 1; re-pin it on 1.1`) |

With `--baseline <tag>` the gate also blocks when a baselined assertion is missing from the report (`block_on_removal`, allowlist with `allow_removed`). Exceeding the soft-fail budget adds a `budget_exceeded` violation whether or not a baseline is supplied. See [baselines](baselines.md#removed-and-changed-assertions) and [policies](policies.md).

## 5. Gate merges in GitHub Actions

`attest init --ci github` writes `.github/workflows/attest.yml` with two jobs. The first, `attest`, runs your tests with `contents: read` and no write access. The second, `annotate`, holds `pull-requests: write` and never runs your code.

The `attest` job:

1. **Run tests** has `continue-on-error: true` and `id: tests`, so a failing test does not stop the job before the report is handled.
2. **Write the JUnit report** runs `attest report junit --report attest-report.json --out attest-junit.xml`. **Write the failure viewer** runs `attest view attest-report.json --no-open --out attest-report.html`. **Upload the Attest report** uploads the report, the JUnit file and the HTML page as the `attest-report` artifact. Without traces the page still shows failures, expected versus actual and the baseline metrics. To add the trace tree, uncomment `ATTEST_REPORT_TRACES: "1"` on the test step, on private repositories only: traces hold prompts, outputs and tool arguments, and they are not redacted. See [Failure viewer](viewer.md).
3. **Evaluate the merge policy** treats exit 2 as a warning, fails on exit 1, and fails on evaluator errors such as exit 3.
4. **Fail the job when tests failed** checks `steps.tests.outcome == 'failure'` and exits 1, so a red test is never hidden by `continue-on-error`.

Steps 2 and 3 run with `if: always()`.

The `annotate` job needs `attest`, runs with `if: always()` for pull requests from branches of the same repository, downloads the `attest-report` artifact and:

1. Fetches the engine: `uvx --no-config --exclude-newer <date> --from "attest-ai==<version>" attest doctor`, run from the runner's temporary directory. `pr-annotate` finds an engine but never downloads one; this command downloads the pinned release's engine into `~/.attest/bin/<version>/`. `--no-config` and the directory keep anything the pull request controls (a `uv.toml`, a `pyproject.toml`) from steering the install, and `--exclude-newer` refuses a release newer than the scaffold date.
2. **Annotates the pull request** with the `pr-annotate` action, passing `engine-version`. It posts one summary comment and emits a workflow annotation for each failure and replay miss; see [Annotations and the summary comment](annotations.md). On a fork's pull request the token is read-only, so the job is skipped; the report, JUnit file and HTML page are still in the `attest` job's artifact.

Permissions are minimal and split by job. The workflow grants `contents: read`; only `annotate` adds `pull-requests: write`, which the summary comment needs. The `annotate` job does not restore the cache the test job filled (`enable-cache: false` on `setup-uv`).

Every action in the generated workflow is pinned to a full commit SHA with the release in a trailing comment (`uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1`), not a tag. A tag can be moved to different code after you reviewed it; a SHA cannot. This matters most in the job that holds a write token.

### Keeping the pins current

Pinned actions do not update themselves. Let Dependabot propose the bumps in your repository, so each one is a reviewable diff of a SHA and its version comment (`.github/dependabot.yml`):

```yaml
version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
    groups:
      github-actions:
        patterns: ["*"]
```

Two values in the `annotate` job are not actions, so Dependabot does not touch them: the `attest-ai==<version>` in the engine fetch step and `engine-version`. They must equal the release of the `pr-annotate` pin (the `v1.0.1` in its comment). When you move the `pr-annotate` pin, change the `attest-ai==` pin, `engine-version` and, if you want a fresh bound, the `--exclude-newer` date in the same pull request. To check for a tag-pinned action, run `grep -nE 'uses: *[^ ]+@' .github/workflows/*.yml | grep -vE '@[0-9a-f]{40}( |$)'`; it prints nothing when every action is pinned.

**1.1.0 scaffold limitation:** the generated annotation job still uses v1.0.1, even when the test job installs 1.1.0. The next patch is scheduled to bump it. For 1.1.0 annotation and policy behavior now, use `attest-framework/attest/.github/actions/pr-annotate@101de1cdd703b04abf5fd854f347565fd37715b8 # v1.1.0`, fetch `attest-ai==1.1.0`, set `engine-version: 1.1.0`, and move `--exclude-newer` to after the 1.1.0 publication. The complete action example is in [annotations](annotations.md).

`pr-annotate` also evaluates a policy itself when you pass `policy:`. Its `exit-code` output is the evaluator's exit code; the step fails on 1 (unless `fail-on-violation: false`) and on any code other than 0, 1 and 2. The workflow above evaluates the policy in its own step instead, so the action runs without `policy:`.
