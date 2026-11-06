# Baselines

A baseline is a pinned report-v2: the status, score, cost and duration of every assertion at a point you trust, stored under a tag. `attest policy evaluate --baseline <tag>` compares a new report with it, so a policy can block a change that makes assertions that used to pass fail, or lowers a pass rate measured over fixed-N trials. See [policies](policies.md) for the rules that act on the comparison.

## Pin a baseline

Write a report from a run you trust, then pin it:

<!-- snippet: setup=project -->
```bash
uv run pytest --attest-report=attest-report.json
uv run attest baseline pin --tag main --report attest-report.json
```

For Vitest, write the report with the reporter shown in the [merge gate](merge-gate.md) and pin the same way with `npx attest baseline pin ...`.

`--tag` is any name: a branch, a release, a commit SHA. Pinning a tag that exists replaces it. The report must be report-v2; a v1 report is rejected.

| Command | Does |
|---|---|
| `attest baseline pin --tag <name> --report <path>` | Stores the report's assertions under `<name>` |
| `attest baseline list` | Lists pinned tags |
| `attest baseline show --tag <name>` | Prints the pinned assertions |
| `attest baseline delete --tag <name>` | Removes the tag |

## Where baselines live

Baselines are rows in `attest.db` under `ATTEST_CACHE_DIR` (default `~/.attest/cache`). They live on the machine that pinned them. A CI job starts with an empty cache, so a workflow that compares against a baseline has to restore that directory (for example with a cache step keyed on the baseline tag) or pin the baseline in the same job from the base branch's run before it evaluates the pull request.

## Compare a run with a baseline

<!-- snippet: setup=project -->
```bash
uv run attest policy evaluate --policy attest.policy.yaml --report attest-report.json --baseline main
```

The evaluation names a tag that does not exist as an error (exit 3), not as a pass. A policy that sets `block_on_regression: true` or a `regression` rule needs `--baseline`; without it `attest policy evaluate` exits 3 and names the missing flag. `block_on_regression: false` needs nothing.

`attest policy evaluate` prints its violations as JSON; the comparison reaches the output only through the policy rules. `block_on_regression: true` adds a `block_on_regression` violation naming each assertion that passed in the baseline and does not now (an assertion that was added or removed is not a regression; removal has its own rule below), and a `regression` rule adds a verdict per assertion.

## Removed and changed assertions

A baselined assertion id that is missing from the report is a regression: deleting the assertions that protect a behavior must not turn the gate green. With `--baseline`, `attest policy evaluate` adds a `block_on_removal` violation (exit 1) naming each missing id, unless the policy lists it under `allow_removed`:

```yaml
block_on_removal: true        # default when --baseline is given; false turns the rule off
allow_removed: [3f9a1c0e5b7d2a44]   # ids whose removal is accepted
```

Reordering assertions keeps every id, so it passes. An id is a hash of the test (or `ContinuousEvalRunner` name), the assertion type, the canonical spec and the occurrence index among identical assertions in that test, so editing a spec produces a new id. When the same test still holds an assertion of the same type with a different spec, the missing id is reported as `changed` instead of removed: the policy output lists it under `changed` (`assertion_id`, `new_assertion_id`) and it does not block. Re-pin the baseline to accept the edit.

## Id scheme

Reports and baselines carry `id_scheme: 2`. A baseline pinned before 1.1 uses scheme 1 (ordinal ids); `attest policy evaluate --baseline` refuses to compare against it and exits 3 with `baseline uses id scheme 1; re-pin it on 1.1`, rather than reporting every assertion as removed. The check applies to the head report too: a report without `id_scheme: 2` (written by an SDK older than 1.1) exits 3 with `report uses id scheme 1; re-run with an SDK on 1.1`. Re-pin the tag from a 1.1 report. Rolling back to 1.0 orphans baselines pinned on 1.1.

The same ids key the result history behind `threshold="dynamic"` and drift alerts. History written before 1.1 under ordinal ids matches no assertion, so each baseline restarts at the 10-run cold start ([continuous eval](continuous-eval.md#upgrading)).

## Trials and the regression rule

A baseline pinned from a fixed-N run (`pytest --attest-trials=N`, Vitest `trials()`) keeps each assertion's pass tally. A policy with a `regression` rule compares the new tally with the baseline tally using a Wilson interval and returns `pass`, `regressed` or `inconclusive`, so one flaky trial in twenty does not read as a regression. See [statistical verdicts](statistical-verdicts.md).

## Refresh a baseline

Re-pin the tag from a report you trust when behavior changes on purpose: a new prompt, a re-recorded [fixture](capture.md) or an intentional contract change. Review the regressions first: with `block_on_regression: true`, `attest policy evaluate --baseline <tag>` names them. Then pin.
