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

A policy or report that cannot be read or is invalid exits 1 with the error on stderr; leaving out `--policy` or `--report` exits 2.

## Write a policy

`attest init --ci github` writes a starting `attest.policy.yaml`. The full rule set:

```yaml
# Default severity for rules that do not set their own: block or warn.
severity: block

# Assertions with no verdict. warn (default) exits 2; block exits 1.
not_evaluated: warn

# Fail the gate when the report's total cost exceeds this many dollars.
max_cost_usd: 0.50

# Per-layer caps on failures (layers 1-8; 0 means any layer).
layers:
  3: { max_hard_fails: 0 }
  6: { max_soft_fails: 2, severity: warn }

# Caps by failure class.
failure_classes:
  broken_code: { max: 0 }

# Needs --baseline: block assertions that passed in the baseline and do not now.
block_on_regression: true

# Needs --baseline and fixed-N trials: statistical comparison of pass rates.
regression: { confidence: 0.95, tolerance: 0.05 }
```

Every field is optional. An empty policy has no rules to block on, but the `not_evaluated` rule still applies, so it exits 2 when any assertion was not evaluated. A severity other than `block` or `warn` is a load error, so a typo cannot quietly weaken a rule. A `.json` policy takes the same fields.

## Not evaluated is not pass

An assertion the engine could not judge (`provider_unavailable`, `provider_not_configured`, `simulation`, `missing_evidence`, `short_circuited`, `budget_exhausted`, `judge_abstained`, `replay_miss`) has status `not_evaluated` and a reason. It is never counted as a pass, and it is excluded from pass rates. The policy's `not_evaluated` severity decides what it costs: the default `warn` exits 2 so an outage or a missing key does not block merges; `not_evaluated: block` makes it exit 1.

A [replay miss](replay.md#what-a-miss-says) is `not_evaluated` with reason `replay_miss` when the test could not go live, and a failed test when the replay is strict.

## Baselines

`--baseline <tag>` adds the comparison described in [baselines](baselines.md) and activates `block_on_regression` and `regression`. Both are no-ops without it.

## Render the report for reviewers

The engine renders report-v2 files for CI. Each command takes `--report` one or more times; several reports merge (a project that writes one report per runner passes them all). An unreadable or invalid report exits 2 with the error on stderr.

```bash
# JUnit XML for CI test views: one suite per test file, a <failure> for a failed test,
# a <skipped> carrying the reasons for a test with no verdict.
uv run attest report junit --report attest-report.json --out junit.xml

# Markdown summary for a PR comment.
set +e   # policy evaluate exits 1 or 2 on violations; keep going so the summary renders
uv run attest policy evaluate --policy attest.policy.yaml --report attest-report.json > policy-result.json
uv run attest report summary --report attest-report.json --policy-result policy-result.json

# Failures and replay misses as JSON rows, for annotations.
uv run attest report rows --report attest-report.json --json
```

The summary opens with `### attest: <N> passed · <N> failed · <N> not evaluated`, counted over tests. It follows with:

```text
3 replayed · 0 live model calls · $0.00
2 model calls · 1500 tokens (100 cached) · $0.0123 · retry cost $0.0010
```

The first line counts the agent's own model calls: how many a fixture served and how many went to a provider, priced from the engine's built-in price table (`(+N unpriced)` marks live calls to models it has no price for). The second is Attest's own usage: its judges and embeddings. Then come the not-evaluated counts by reason, a table of failures and replay misses with `file:line`, test, assertion type and explanation (the first 20 rows, then `and N more`), and, when `--policy-result` is given, the policy verdict and its violations. The output is the same for the same inputs.

`report rows --json` prints one array of `{file, line, test_id, assertion_id, type, status, reason, explanation}`: hard failures, and replay misses whether they failed the test (strict) or left it not evaluated (auto).
