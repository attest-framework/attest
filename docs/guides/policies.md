# Policies

A policy is the file that decides whether a report lets a pull request merge. `attest policy evaluate` reads a report-v2 and a policy, prints the violations as JSON, and exits with the verdict:

```bash
uv run attest policy evaluate --policy attest.policy.yaml --report attest-report.json
```

| Exit | Meaning |
|---|---|
| 0 | Every rule passed |
| 1 | A rule with severity `block` was violated |
| 2 | Only rules with severity `warn` were violated |

A policy or report that cannot be read or is invalid exits 3 with the error on stderr; leaving out `--policy` or `--report` also exits 3. Exit 3 is neither a warning (2) nor a block (1), and CI steps that treat only 0, 1 and 2 as verdicts fail on it.

## Write a policy

`attest init --ci github` writes a starting `attest.policy.yaml`. The full rule set:

```yaml
# Default severity for rules that do not set their own: block or warn.
severity: block

# Assertions with no verdict. warn (default) exits 2; block exits 1.
not_evaluated: warn

# Fail the gate when the report's total cost exceeds this many dollars.
max_cost_usd: 0.50

# Per-layer caps on failures (keys 1–6, and 8 for plugin assertions; 0 means any layer).
layers:
  3: { max_hard_fails: 0 }
  6: { max_soft_fails: 2, severity: warn }

# Caps by failure class.
failure_classes:
  broken_code: { max: 0 }

# Needs --baseline: block assertions that passed in the baseline and do not now.
block_on_regression: true

# Needs --baseline: block baselined assertion ids missing from the report.
# On by default when --baseline is given; list the ids whose removal is accepted.
block_on_removal: true
allow_removed: []

# Needs --baseline and fixed-N trials: statistical comparison of pass rates.
regression: { confidence: 0.95, tolerance: 0.05 }

# Needs --mutation <mutate.json>: the mutation score from `attest mutate --json`
# must reach this ratio (0 to 1). Severity defaults to the policy severity.
min_mutation_score: { score: 0.8, severity: block }
```

Every field is optional. An empty policy has no rules to block on, but the `not_evaluated` rule still applies, so it exits 2 when any assertion was not evaluated. A severity other than `block` or `warn` is a load error, so a typo cannot quietly weaken a rule. A `.json` policy takes the same fields.

## Not evaluated is not pass

An assertion the engine could not judge (`provider_unavailable`, `provider_not_configured`, `simulation`, `missing_evidence`, `short_circuited`, `budget_exhausted`, `judge_abstained`, `judge_malformed`, `provider_auth`, `replay_miss`) has status `not_evaluated` and a reason. It is never counted as a pass, and it is excluded from pass rates. The policy's `not_evaluated` severity decides what it costs: the default `warn` exits 2 so an outage or a missing key does not block merges; `not_evaluated: block` makes it exit 1.

A [replay miss](replay.md#what-a-miss-says) is `not_evaluated` with reason `replay_miss` when the test could not go live, and a failed test when the replay is strict.

## Mutation score

`min_mutation_score` gates on how well the suite's contracts catch behavior mutants of your fixtures ([mutation](mutation.md)). Pass the `--json` output of `attest mutate`:

```bash
uv run attest mutate tests/attest/fixtures/*.attest.json --contracts .attest/contracts --json > mutation.json
uv run attest policy evaluate --policy attest.policy.yaml --report attest-report.json --mutation mutation.json
```

A score below `score` is a violation named `min_mutation_score` with the rule's severity, so it exits 1 (`block`) or 2 (`warn`). A report with no score (no mutant could be scored) cannot meet a threshold and is also a violation. `score` must be between 0 and 1 and is required; a policy that sets the rule without `--mutation` is a load error (exit 3), so the gate cannot be skipped by forgetting the file. `--mutation` without the rule changes nothing.

## Budget exceeded

A run whose engine session passed `ATTEST_MAX_SOFT_FAILURES` still reports every assertion, hard failures included, and the report carries `budget_exceeded: true`. `attest policy evaluate` adds a `budget_exceeded` violation with the policy severity (default `block`), so a breach never hides a failure and never passes silently.

## Baselines

`--baseline <tag>` adds the comparison described in [baselines](baselines.md) and activates `block_on_regression` and `regression`, and turns on `block_on_removal` (see [removed and changed assertions](baselines.md#removed-and-changed-assertions)). A policy that sets either rule without `--baseline` is a load error: `attest policy evaluate` exits 3 and names `--baseline` on stderr, so the gate cannot be skipped by forgetting the flag. `block_on_regression: false` needs no baseline. `--baseline` always computes the baseline delta, with or without either rule, and a tag with no pinned baseline exits 3 (`no baseline pinned with tag ...`).

## Report completeness

A report that records `tests_collected` above 0 and an empty `tests` array lost its results (a crashed worker or reporter), so `attest policy evaluate` exits 3 instead of passing an empty run. A report without `tests_collected` is accepted. The pytest plugin writes the field from pytest's own collected count, independent of the rows, so the check protects pytest reports (under pytest-xdist the controller writes one report that merges every worker's tests). The Vitest reporter sets `tests_collected` to the number of rows it reports, so the check cannot fire on Vitest reports. A test module that fails to import is in neither the rows nor the count, so this check does not catch it.

## Render the report for reviewers

The engine renders report-v2 files for CI. Each command takes `--report` one or more times; several reports merge (a project that writes one report per runner passes them all). An unreadable or invalid report exits 2 with the error on stderr.

```bash
# JUnit XML for CI test views: one suite per test file, a <failure> for a failed test,
# a <skipped> for a skipped test, naming the reasons when its assertions were not evaluated.
uv run attest report junit --report attest-report.json --out junit.xml

# Markdown summary for a PR comment.
set +e   # policy evaluate exits 1 or 2 on violations; keep going so the summary renders (exit 3 is an error: stop)
uv run attest policy evaluate --policy attest.policy.yaml --report attest-report.json > policy-result.json
[ $? -le 2 ] || exit 1
uv run attest report summary --report attest-report.json --policy-result policy-result.json

# Failures and replay misses as JSON rows, for annotations.
uv run attest report rows --report attest-report.json --json
```

The summary opens with `### attest: <N> passed · <N> failed · <N> not evaluated`, counted over tests. A test is not evaluated when it was skipped and every one of its assertion rows is `not_evaluated`; any other skipped test (a plain skip, or an expected failure such as pytest `xfail` or Vitest `it.fails`) is counted in a trailing ` · N skipped`, shown only when N is above zero. Expected failures are never listed as failures. It follows with:

```text
3 replayed · 0 live model calls · $0.00
2 model calls · 1500 tokens (100 cached) · $0.0123 · retry cost $0.0010
```

The first line counts the agent's own model calls: how many a fixture served and how many went to a provider, priced from the engine's built-in price table (`(+N unpriced)` marks live calls to models it has no price for). The second is Attest's own usage: its judges and embeddings. Then come the not-evaluated counts by reason, a table of failures and replay misses with `file:line`, test, assertion type and explanation (the first 20 rows, then `and N more`), and, when `--policy-result` is given, the policy verdict and its violations. The output is the same for the same inputs.

`report rows --json` prints one array of `{file, line, test_id, assertion_id, type, status, reason, explanation}`: hard failures, and replay misses whether they failed the test (strict) or left it not evaluated (auto).
