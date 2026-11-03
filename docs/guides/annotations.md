# Annotations and the summary comment

`pr-annotate` turns report-v2 files into what a reviewer sees on a pull request: one summary comment, plus an annotation on the file and line of each failing test. `attest init --ci github` wires it into the workflow ([merge gate guide](merge-gate.md)). To use it directly:

`pr-annotate` v2 ships in `1.0.0-rc.3`; pin the tag of the release you use (`attest init --ci github` writes it).

```yaml
permissions:
  contents: read
  pull-requests: write
steps:
  - uses: attest-framework/attest/.github/actions/pr-annotate@v1.0.0-rc.3
    if: always() && github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository
    with:
      report: attest-report.json
      policy: attest.policy.yaml   # optional
```

| Input | Meaning |
|---|---|
| `report` | One report-v2 path, or several separated by newlines or spaces (paths cannot contain spaces). |
| `policy`, `baseline-tag` | Optional. Runs `attest-engine policy evaluate` on each report. |
| `engine-binary` | The engine. The default name `attest-engine` is resolved as `ATTEST_ENGINE_PATH`, then `PATH`, then `~/.attest/bin/<engine-version>/attest-engine`, the SDK's download cache. Run an `attest` command (for example the JUnit step) before the action so the engine is cached. |
| `engine-version` | The engine version whose cache directory is used. Defaults to the version of the action itself (the repository `VERSION` at the action's tag). |
| `github-token` | Defaults to the workflow token. Used only to read and write the summary comment. |
| `fail-on-violation` | `true` (default) fails the step when the policy exit is 1. |

Output `exit-code` is the policy exit code (0 when no `policy`).

## The summary comment

The comment is rendered by `attest report summary` over every report and the policy result: pass, fail and not-evaluated counts (with reasons), replayed versus live model calls, `model calls · tokens · $ · retry cost`, and the failures with `file:line`. A link to the workflow run follows. Every string that comes from the report (test ids, paths, explanations, not-evaluated reasons, policy rules and details) is shown as an inline code span, so agent output cannot ping users, link issues or inject HTML. A summary over GitHub's size limit is cut at a line boundary and the run link follows the cut.

There is one comment per pull request. The first run creates it; later runs update it in place. The action finds it by the hidden marker `<!-- attest:pr-annotate -->`. Only marker comments written by the token's own login are updated: the Actions bot (login `github-actions[bot]`, type `Bot`) for the workflow token, the user for a personal token. A marker planted by anyone else is ignored and never edited; this login filter is the protection, since a token with write access could otherwise edit anyone's comment. If GitHub cannot tell who the token is (any error other than the workflow token's 403 "Resource not accessible by integration"), no comment is posted and a warning is printed.

It is posted only on `pull_request` events from the same repository. On a fork pull request, or when the token cannot write (403), the action posts nothing and prints a notice (fork or non-pull-request event) or a warning (the API refused the write). The summary is always appended to the job summary (`$GITHUB_STEP_SUMMARY`), so it is readable either way. A comment that fails to post never fails the step.

## Annotations

Each row from `attest report rows` becomes a workflow command, which GitHub shows in the checks list and inline on the line in the diff:

| Row | Annotation |
|---|---|
| hard failure | `::error` |
| replay miss (`not_evaluated`, reason `replay_miss`) | `::error` |
| soft failure | `::warning` |
| other not-evaluated | `::warning` |

Annotations are not limited to lines in the diff, unlike review comments, so a failing test on an untouched line still annotates.

GitHub shows 10 errors and 10 warnings per step. The action emits at most 10 errors and 9 warnings (one warning slot stays free for a comment failure); when more exist, the tenth is one annotation counting the rest. The full list is in the summary and the report artifact. Messages are capped at 1000 characters, control characters are removed, and `%`, line breaks, `:` and `,` are escaped as the workflow-command format requires. Report content is treated as untrusted: it can contain agent output.

### Paths

Rows carry `file` relative to the directory the tests ran from (pytest `rootdir`, Vitest `root`). GitHub needs a path relative to the repository root. The action prefixes each row's `file` with the report's directory relative to `GITHUB_WORKSPACE`:

| Report | Row `file` | Annotated path |
|---|---|---|
| `attest-report.json` | `tests/test_refund.py` | `tests/test_refund.py` |
| `py/attest-report.json` | `tests/test_refund.py` | `py/tests/test_refund.py` |

Write the report into the directory you run the tests from. If the prefixed path does not exist in the workspace but the unprefixed one does, the unprefixed path is used. Each report is resolved against its own directory, so reports from several subdirectories can go in one `report:` input. A row without a valid location (absolute, `..`, or missing) is annotated without a file, never at a guessed line.

## Several reports

With several reports the policy runs on each and the worst exit wins: an evaluator error (any code other than 0, 1 and 2) first, then 1, then 2, then 0. The result of that report feeds the summary. It is written to a temporary file under `RUNNER_TEMP`, not the workspace, and is not echoed to the log.

## Changed in v2

Earlier versions posted a pull request review with inline comments, which can only target lines in the diff. v2 posts the summary comment and annotations instead, and needs the engine (for `report summary` and `report rows`) even without a `policy`.
