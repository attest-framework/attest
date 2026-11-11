# Annotations and the summary comment

`pr-annotate` turns report-v2 files into what a reviewer sees on a pull request: one summary comment, plus file-and-line annotations for hard-failing assertions and replay misses. `attest init --ci github` wires it into the workflow ([merge gate guide](merge-gate.md)). To use it directly:

Pin `pr-annotate` to the full commit SHA of the release you use, with the release in a trailing comment. A tag can be moved to other code; a SHA cannot, and this action runs with a token that can write to the pull request. The example below uses **1.1.0**.

```yaml
permissions:
  contents: read
  pull-requests: write
steps:
  - uses: attest-framework/attest/.github/actions/pr-annotate@101de1cdd703b04abf5fd854f347565fd37715b8 # v1.1.0
    if: always() && github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository
    with:
      report: attest-report.json
      engine-version: 1.1.0
      policy: attest.policy.yaml   # optional
```

**1.1.0 scaffold limitation:** `attest init --ci github` still writes the v1.0.1 action pin (`5d5b748b13da7ba034b3acfce631cee8fcae720c`), fetches `attest-ai==1.0.1` and sets `engine-version: 1.0.1`. The scaffold pin is scheduled to move in the next patch. To use 1.1.0 now, update all three together and move the fetch step's `--exclude-newer` date to after the 1.1.0 publication; see [keeping the pins current](merge-gate.md#keeping-the-pins-current).

| Input | Meaning |
|---|---|
| `report` | One report-v2 path, or several separated by newlines or spaces (paths cannot contain spaces). |
| `policy`, `baseline-tag` | Optional. Runs `attest-engine policy evaluate` on each report. |
| `engine-binary` | The engine. The default name `attest-engine` is resolved as `ATTEST_ENGINE_PATH`, then `PATH`, then `~/.attest/bin/<engine-version>/attest-engine`, the SDK's download cache. Run an `attest` command (for example the JUnit step) before the action so the engine is cached. |
| `engine-version` | The engine version whose cache directory is used. Defaults to the version of the action itself (the repository `VERSION` at the action's commit). Keep it equal to the release in the action's version comment and to the `attest-ai==` version that fetched the engine. |
| `github-token` | Defaults to the workflow token. Used only to read and write the summary comment. |
| `comment-author` | The login whose summary comment is updated. Empty (default) means the token's own: `github-actions[bot]` for the workflow token, the user for a personal token. Set `<app-slug>[bot]` when `github-token` is a GitHub App installation token. |
| `fail-on-violation` | `true` (default) fails the step when the policy exit is 1. |

Output `exit-code` is the policy exit code (0 when no `policy`).

## The summary comment

The comment is rendered by `attest report summary` over every report and the policy result: pass, fail and not-evaluated counts (with reasons), replayed versus live model calls, `model calls · tokens · $ · retry cost`, and the failures with `file:line`. A link to the workflow run follows. Every string that comes from the report (test ids, paths, explanations, not-evaluated reasons, policy rules and details) is shown as an inline code span, so agent output cannot ping users, link issues or inject HTML. A summary over GitHub's size limit is cut at a line boundary and the run link follows the cut.

There is one comment per pull request. The first run creates it; later runs update it in place. The action finds it by the hidden marker `<!-- attest:pr-annotate -->`. Only marker comments written by the token's own login are updated: the Actions bot (login `github-actions[bot]`, type `Bot`) for the workflow token, the user for a personal token. A marker planted by anyone else is ignored and never edited; this login filter is the protection, since a token with write access could otherwise edit anyone's comment. A GitHub App installation token gets the same 403 as the workflow token from `GET /user`, so the action cannot tell it from the Actions bot; for an App token set `comment-author: <app-slug>[bot]` (an App's comments have type `Bot`) and only that login's marker comments are updated. If GitHub cannot tell who the token is (any error other than the workflow token's 403 "Resource not accessible by integration"), no comment is posted and a warning is printed.

It is posted only on `pull_request` events from the same repository. On a fork pull request, or when the token cannot write (403), the action posts nothing and prints a notice (fork or non-pull-request event) or a warning (the API refused the write). The summary is always appended to the job summary (`$GITHUB_STEP_SUMMARY`), so it is readable either way. A comment that fails to post never fails the step.

## Annotations

Each row from `attest report rows` becomes a workflow command, which GitHub shows in the checks list and inline on the line in the diff:

| Row | Annotation |
|---|---|
| hard failure | `::error` |
| replay miss (`not_evaluated`, reason `replay_miss`) | `::error` |
| strict replay miss (synthesized from a failed test) | `::error` |

Annotations are not limited to lines in the diff, unlike review comments, so a failing test on an untouched line still annotates.

In 1.1.0, `report rows` emits hard failures and replay misses only, excluding expected failures and hard-fail trial rows from tests that passed their required pass rate. Other not-evaluated results and soft failures remain in the summary/report; they do not produce inline warning annotations.

GitHub shows 10 errors and 10 warnings per step. The action emits at most 10 errors and 9 warnings (one warning slot stays free for a comment failure); when more exist, the tenth is one annotation counting the rest. The full list is in the summary and the report artifact. Messages are capped at 1000 characters, control characters are removed, and `%`, line breaks, `:` and `,` are escaped as the workflow-command format requires. Report content is treated as untrusted: it can contain agent output.

### Paths

Rows carry `file` relative to the directory the tests ran from (pytest `rootdir`, Vitest `root`). GitHub needs a path relative to the repository root. The action prefixes each row's `file` with the report's directory relative to `GITHUB_WORKSPACE`:

| Report | Row `file` | Annotated path |
|---|---|---|
| `attest-report.json` | `tests/test_refund.py` | `tests/test_refund.py` |
| `py/attest-report.json` | `tests/test_refund.py` | `py/tests/test_refund.py` |

Write the report into the directory you run the tests from. If the prefixed path does not exist in the workspace but the unprefixed one does, the unprefixed path is used. Each report is resolved against its own directory, so reports from several subdirectories can go in one `report:` input. A row without a valid location (absolute, `..`, or missing) is annotated without a file, never at a guessed line.

## Several reports

With several reports and a `policy`, the policy runs on each and the summary's policy line is the aggregate, not the worst report alone:

- the exit code is the worst one: an evaluator error (any code other than 0, 1 and 2) first, then 1, then 2, then 0;
- `passed` is false unless every report passed;
- the violations are those of every report, each rule prefixed with the report it came from (`<report>: max_hard_fails`);
- the regression verdicts stay per report;
- a **Policy by report** list under the policy line gives each report's own result, for example `` - `py/attest-report.json`: fail (exit 1), 2 violation(s) ``. A report whose evaluator printed nothing usable still counts as a violation.

A single report is passed through unchanged and has no list. The result of each evaluation is written to a temporary file under `RUNNER_TEMP`, not the workspace, and is not echoed to the log.

## Changed in v2

Earlier versions posted a pull request review with inline comments, which can only target lines in the diff. v2 posts the summary comment and annotations instead, and needs the engine (for `report summary` and `report rows`) even without a `policy`.
