# Debug a failing, skipped or not-evaluated attest test

Goal: name the cause of a red, skipped or blocked result and fix the cause, not the assertion. Background: `attest guide verdicts`, `attest guide replay`, `attest guide policies`, `attest guide trace-check`.

## 1. Reproduce with a report

```bash
uv run pytest <test path> --attest-strict --attest-report=attest-report.json
ATTEST_STRICT=1 npx vitest run <test path> --reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=attest-report.json
```

Add the replay flags the repo uses (`--attest-replay=strict --attest-egress=block`, Vitest `ATTEST_REPLAY=strict ATTEST_EGRESS=block`). Run in CI conditions: no real provider key, `CI=1` when the failure is CI-only.

## 2. Read the result

```bash
uv run attest report rows --report attest-report.json --json      # [{file, line, test_id, assertion_id, type, status, reason, explanation}]
uv run attest report summary --report attest-report.json          # the PR summary Markdown
uv run attest policy evaluate --policy attest.policy.yaml --report attest-report.json
```

`report rows` lists hard failures and replay misses with their test file and line. The pytest/Vitest diagnostic names the layer, the trace path, expected and actual:

```text
Attest diagnostic — 1 of 4 assertions failed:
  FAIL assert_2d1d647b — L4 content
      trace path: output.message
      expected:   contains "store credit"
      actual:     Your refund of $42.00 has been issued.
```

| Status | Test outcome | Meaning |
| --- | --- | --- |
| `hard_fail` | failed | the assertion is violated |
| `not_evaluated` + reason | skipped (failed under `--attest-strict` / `ATTEST_STRICT=1`) | no verdict; the reason says why |
| `soft_fail` | passed, listed under soft failures | a `soft` assertion missed |
| `pass` | passed | |

## 3. Fix by cause

`hard_fail`: decide whether the agent or the assertion is wrong. If the explanation describes the bug the contract exists for (`issue_refund called 2× for order_id=O-1042 (max 1)`, `effect violated: 2 refunds for O-1042 (expected 1)`), fix the agent. Change an assertion only when it encodes a wrong rule, and tell the user.

`not_evaluated` reasons (never treat as pass):

| Reason | Cause | Fix |
| --- | --- | --- |
| `missing_evidence` | the trace lacks the field: tool arguments for `per`/`arg`, `metadata.cost_usd`, tool errors on a v1 trace; or `world(None)` | `attest trace check <export or fixture> --json` lists coverage gaps; record the attributes, convert with the OTLP adapters (trace v2), or start the world |
| `replay_miss` | `auto` mode, a request matched no recording, no key to go live | see replay misses below |
| `provider_not_configured` | judge or embedding assertion, no provider configured | expected in CI with zero model calls; keep the policy's `not_evaluated: warn` or move the assertion to a deterministic check; `attest doctor --json` shows configuration |
| `provider_unavailable` | provider call failed | transient; do not convert to pass |
| `simulation` | `ATTEST_SIMULATION=1` disables layers 5 and 6 | expected in simulation runs |
| `short_circuited` | a layer 1–4 assertion in the same batch hard-failed | fix that failure first |
| `budget_exhausted` | `ATTEST_MAX_JUDGE_CALLS`, `ATTEST_BUDGET_USD` or the test budget is used up | raise the budget only with the user's agreement |
| `judge_abstained` | the judge said the output lacks the evidence its rubric needs | the reason is in the explanation |

## 4. Replay misses

A strict miss fails the test with the first component that changed:

```text
replay miss: system prompt changed (hash a1b2c3d→e4f5a6b); re-record with `attest capture --refresh inc-1042`
```

Other forms: `messages changed`, `tools changed`, `params changed`, `model changed ("gpt-4.1"→"gpt-5.4")`, `the agent made more model calls than the 3 recorded`, `the agent made an extra identical call`.

- The change is unintended (a test input differs from the captured run, a prompt edit slipped in): revert it.
- The change is intended: re-record with `capture-incident.md` step 5 (`attest capture --refresh <name> --yes` after reviewing the command; that run calls the upstream model, so get approval).
- `attest egress guard: connection to … blocked`: a client was built before the replay started or with a hard-coded base URL.

## 5. Policy exit codes

`attest policy evaluate` prints `{violations, exit_code, passed}` as JSON:

| Exit | Meaning | Action |
| --- | --- | --- |
| 0 | every rule passed | |
| 1 | a `block` rule was violated | read `violations`; fix the failures they name |
| 2 | only `warn` rules were violated (default: `not_evaluated`) | report the reasons; not a pass to hide |
| 3 | usage error or the policy/report could not be loaded | check paths and YAML; `attest help --json` for flags |

Other commands: 0 success, 1 the result is a failure, 2 usage error (`attest guide cli` has the full table).

## 6. Trace problems

```bash
uv run attest trace check <export.otlp.json | fixture.attest.json> --json
uv run attest trace convert <export.otlp.json> > trace.json
```

Exit 1 is an integrity error (`orphan_span`, `parent_cycle`, …) naming the span; coverage gaps do not fail the check but predict `missing_evidence`.

Report: the failing test, the status and reason or explanation, the cause, the fix, and the rerun command with its result line.
