---
name: attest
description: Test AI agents with Attest (attest-ai on PyPI, @attest-ai/core and @attest-ai/vitest on npm). Use when asked to add a behavior or side-effect contract to an agent (for example "prevent duplicate refunds"), turn an incident or a run into a regression test, debug a failing, skipped or not-evaluated attest test, set up an attest merge gate in CI, or migrate eval suites from Promptfoo or DeepEval to attest.
---

# Attest

Attest checks what an agent did (tool calls, side effects, structured output) with deterministic assertions, and runs the agent's real code in CI against recorded model responses, so a pull request check makes zero model calls. Every result is `pass`, `soft_fail`, `hard_fail` or `not_evaluated` with a reason.

## Route the task

| The user asks to | Read |
| --- | --- |
| add a contract or test for an agent behavior or incident ("never refund twice", "always look up the order first", "one refund per order") | `references/write-contracts.md` |
| turn an incident, a bad run or a good run into a fixture and a test | `references/capture-incident.md` |
| explain or fix a failing, skipped or `not_evaluated` attest test, a replay miss, a policy exit code | `references/debug-failure.md` |
| gate merges in CI, annotate pull requests, run only affected tests | `references/ci-setup.md` |
| move Promptfoo or DeepEval tests to attest | `references/migrate.md` |
| write a project-specific verify skill for this repo's agent | the `attest-verify-generator` skill |

A request often spans two rows: an incident with no fixture yet is `capture-incident.md`, then `write-contracts.md`.

## First steps in a repo

1. Find the language and runner. Python: `pyproject.toml` with `attest-ai`, tests run with `uv run pytest`. TypeScript: `package.json` with `@attest-ai/core` and `@attest-ai/vitest`, tests run with `npx vitest run` (or the repo's package manager, from its lockfile). The CLI is `uv run attest …` or `npx attest …`; below, `attest` means whichever applies.
2. Check the install:

   ```bash
   uv run attest doctor --json        # or: npx attest doctor --json
   ```

   It reports the engine version, capabilities and provider configuration. A missing engine or a version mismatch is the first thing to fix.
3. List commands, flags, `--json` schemas and exit codes instead of guessing them:

   ```bash
   uv run attest help --json
   ```

4. Read the guide for the topic before writing code. Guides ship inside the engine and print offline:

   ```bash
   uv run attest guide                  # list topics
   uv run attest guide behavior-contracts
   ```

   Topics: `agents`, `annotations`, `baselines`, `behavior-contracts`, `capture`, `cli`, `configuration`, `framework-adapters`, `judges`, `merge-gate`, `multi-agent`, `policies`, `replay`, `selection`, `simulation`, `statistical-verdicts`, `trace-check`, `verdicts`, `world-fixtures`.

5. Look for existing attest material before creating any:

   ```bash
   git ls-files '*.attest.json' '*world.yaml' '*.otlp.json' '*attest.policy.yaml' 'incidents/*'
   git grep -l -E 'attest_replay|useReplay|attest_world|useWorld|tool_cardinality|toolCardinality'
   ```

## Rules

- **Zero model calls in tests and CI.** Run the agent under replay (`attest_replay` / `useReplay`) with `--attest-replay=strict --attest-egress=block` (Vitest: `ATTEST_REPLAY=strict ATTEST_EGRESS=block`), or assert on a frozen trace or fixture. Do not call a paid model to make a test pass. A live capture is a deliberate, local step the user runs or approves.
- **Never write secrets.** Do not paste API keys into tests, fixtures, workflows or chat. Placeholder values (`OPENAI_API_KEY=replay-needs-no-key`) are enough under replay. Run `attest capture --check-redaction <fixture>` before committing a fixture.
- **Prefer deterministic layers.** Trace contracts and world effects (layer 3), schema (1), constraints (2) and content (4) are free and repeatable. Use an LLM judge (layer 6) only for what nothing else can check, and say so to the user.
- **Prefer replay and fixtures over mocks.** Reuse the repo's fixtures (`tests/attest/fixtures/*.attest.json`), worlds (`*.world.yaml`) and frozen traces (`incidents/**/*.otlp.json`).
- **Use `--json`** on every attest command whose output you parse. On success each prints exactly one JSON document to stdout; diagnostics go to stderr. On an error nothing is written to stdout: read the exit code and stderr. Exit codes: 0 success, 1 the result is a failure, 2 usage error. `attest policy evaluate`: 0 pass, 1 block, 2 warn, 3 usage or load error.
- **Nothing interactive.** Under `--json`, with `CI` set or without a terminal, commands do not prompt. `attest capture --refresh` needs `--yes` there; read the recorded command it prints before passing `--yes`.
- **`not_evaluated` is an outcome, not a pass.** It always carries a reason (`missing_evidence`, `replay_miss`, `provider_not_configured`, `simulation`, …). Report it with its reason and fix the cause. Never convert it to a pass, never delete the assertion to get green, never weaken an assertion with `soft=True` without the user's agreement. Run tests with `--attest-strict` (Vitest `ATTEST_STRICT=1`) so a `not_evaluated` result fails instead of skipping.
- **A reproduction is not a fix.** A test that asserts the incident's contract still `hard_fail`s on the frozen incident trace (`inspect()`) proves the contract catches it; the contract that guards the current agent uses `evaluate()` and passes.
- **Done means a command ran.** Finish with the exact test command, its result line, and any `not_evaluated` reasons.
