# Capture an incident or a run as a fixture

Goal: a committed fixture `tests/attest/fixtures/<name>.attest.json` (trace, recorded model exchanges, drafted contracts, verification) and a drafted test that runs those contracts without the agent. Background: `attest guide capture`, `attest guide trace-check`.

## 1. Pick the source

| You have | Command | Model calls | Replayable |
| --- | --- | --- | --- |
| a command that reproduces the run (`make incident`, `npm run incident`, a CLI) | `attest capture --name <name> [--expect-fail] -- <command>` | yes, forwarded to the upstream | yes |
| the same, plus an external HTTP system modeled as a world | add `--world <file.world.yaml>` | yes | yes, with `--world` / `attest_world` |
| only an OTLP/JSON export of the incident | `attest capture --name <name> [--expect-fail] --from <export.otlp.json>` | none | no (trace and contracts only) |
| an agent that runs in-process (notebook, script) | `attest.record("<name>", expect_fail=True)` / `record("<name>", fn, { expectFail: true })` | yes | yes |

A command capture forwards each model call to the upstream named by `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` in the current environment (default: the real provider). That costs money. Before running it, tell the user which upstream it hits and get approval, unless the repo points it at a local scripted model server (look in `Makefile`, `stack.mk`, `.env.example`). Never put a key on the command line: capture records the command; keys belong in the environment.

Requirements on the agent: its model client reads `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` when constructed, and it exports spans over OTLP/HTTP to `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`. Flush the tracer provider before the process exits. Without spans the capture fails (`no spans were received`). Without a base-URL-aware client the fixture has no exchanges and cannot replay.

## 2. Capture

`--expect-fail` marks the run as an incident: the fixture is verified when at least one drafted contract fails, and those are recorded as expected failures. Without it, the run is a known-good run and every drafted contract must pass.

```bash
uv run attest capture --name inc-1042 --expect-fail --world world/payments-inc-1042.world.yaml -- make incident
npx attest capture --name inc-1042 --expect-fail -- npm run incident
```

Under `--json` the command's own stdout goes to stderr and attest prints one object: `fixture`, `name`, `verified`, `expect_fail`, `replay_capable`, `exchanges`, `steps`, `exit_code`. Exit 0: fixture verified. Exit 1: written but not verified (or no fixture could be built); stderr names the reason. Exit 2: usage. The command's own exit code is recorded in the fixture, not propagated.

Read the printed contract table. For an incident, exactly the contract that names the bug should be `hard_fail` (`cardinality-issue_refund-per-order_id  issue_refund called 2× for order_id=O-1042 (max 1)`). If the incident is not among the failures, the drafted contracts do not express it: write it by hand (`write-contracts.md`).

## 3. Check the fixture before committing

```bash
uv run attest capture --check-redaction tests/attest/fixtures/inc-1042.attest.json   # exit 1 = a secret is present
uv run attest trace check tests/attest/fixtures/inc-1042.attest.json --json           # integrity and coverage
git diff --stat tests/attest/
```

Run `--check-redaction` with the same secrets in the environment as the capture. On a hit, do not commit; report the JSON path it prints, never the value. Coverage gaps in `trace check` (no `cost`, no tool `content`) predict `not_evaluated: missing_evidence` for assertions that need them.

## 4. Run the drafted test

Capture writes `tests/attest/test_<name>.py` or `tests/attest/<name>.test.ts` next to the fixture directory, only for a verified fixture and never over an existing file.

```bash
uv run pytest tests/attest/test_inc_1042.py --attest-strict
ATTEST_STRICT=1 npx vitest run tests/attest/inc-1042.test.ts
```

An incident reproduction test uses `inspect()` and asserts each contract's recorded status, so it passes while proving the contract catches the incident. It does not guard the current agent. Next: `write-contracts.md` to replay the current agent against this fixture with `evaluate()`.

## 5. Refresh after an intended agent change

A replay miss names the refresh command. It re-runs the command recorded in the fixture (`source.command`) with its original flags:

```bash
uv run attest capture --refresh inc-1042          # prints the command, asks y/N at a terminal
uv run attest capture --refresh inc-1042 --yes    # non-interactive: CI, --json, agents
```

Before `--yes`, read the recorded command in the fixture (`source.command`) and confirm it is the expected one: whoever can edit the fixture can change what runs. Refresh refuses fixtures captured with `--from` or in-process, fixtures whose command arguments were redacted or templated, and fixtures with a `world` section; re-run the original `attest capture … --world <file>` command instead. Review the fixture diff and rerun its tests afterwards.

## Failure modes

| Symptom | Fix |
| --- | --- |
| exit 1, `no spans were received` | the agent does not export OTLP/HTTP, or exports to a fixed endpoint instead of `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` |
| `exchanges: 0 recorded` | the model client has a hard-coded base URL; read it from `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` |
| normal capture of an incident verifies with `at_most=2` | the run is an incident: re-capture with `--expect-fail` |
| `--expect-fail` capture unverified: no contract failed | the drafted contracts miss the bug; write the contract by hand |
| exit 2, `capture: refusing to run it without confirmation` | non-interactive context (`CI` set, `--json`, no terminal): read the printed command, then add `--yes` |
