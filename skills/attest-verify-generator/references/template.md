# Template: verify-<agent>/SKILL.md

Render this template with the facts from discovery. Replace every `<…>`; delete rows and sections marked optional when they do not apply. Keep commands exactly as verified.

````markdown
---
name: verify-<agent>
description: Verify the <agent> agent offline with attest, with zero model calls. Use after changing <agent>'s prompt, tools, model settings or <side-effect code path>, before committing such a change, or when asked whether <agent> still <key rule, e.g. "charges each order at most once">.
---

# Verify <agent>

<One paragraph: what the agent does, the entry point, the tools it calls and the external systems it changes.>

General attest procedures: the `attest` skill, `attest help --json` and `attest guide <topic>`. This skill holds only this repository's facts.

## Facts

| Item | Value |
| --- | --- |
| Language and runner | <Python, pytest via `uv run pytest` / TypeScript, Vitest via `npx vitest run` or `pnpm exec vitest run`> |
| Entry point | `<module.function(args)>` |
| Tools (business key) | `<tool>(<args>)` (key `<arg>`), … |
| Side effects | <system> at `$<BASE_URL_ENV>` |
| Fixtures | `<path>.attest.json`: <incident reproduction (expect-fail) / good run>, recorded from `<source.command>` |
| Worlds (optional) | `<path>.world.yaml`: sets `<BASE_URL_ENV>`; faults: <fault on resource/route> |
| Incidents (optional) | `<ID>`: <one line>; frozen trace `<path>` |
| Required environment | `<VAR>=<placeholder>` … (placeholders only; replay needs no real key) |
| Capture upstream | <local scripted server started by `<command>` / paid provider: needs user approval> |

## Run (zero model calls)

```bash
<exact command 1, e.g. make test>
<exact command 2>
```

Expected: <`N passed`> and `attest replay: N replayed · 0 live model calls · $0.00`. Any `not_evaluated` reason or a live call count above 0 is a failure to report, not a pass.

## Contracts in place

| Rule | Contract | Test |
| --- | --- | --- |
| <e.g. one charge per order> | <`tool_cardinality("charge_card", per="order_id", at_most=1)`; `world(state).count("charges", equals=1, order_id=…)`> | `<path>::<test>` |

## Extend

- **New rule on tool calls:** add a method to the chain in `<test file>` (`attest guide behavior-contracts`), rerun the Run commands.
- **New rule on side effects (optional):** add `.world(state).count|unique|idempotent(...)` in `<test file>` against `<world path>` (`attest guide world-fixtures`).
- **New incident:** `<uv run|npx> attest capture --name <id> --expect-fail <--world path> -- <command>`; it calls <capture upstream>, so <get approval / start the local server first>. Then add the guarding contract next to `<test file>`.
- **Intended prompt, tool or model change:** strict replay fails with `replay miss: … changed`. Show the human `source.command` from `<fixture path>` with its SHA-256 (`capture-incident.md` step 5) and stop for their confirmation; never pass `--yes` from a skill. After the human refreshes, review the fixture diff and rerun.

## Rules

- No real API keys in commands, tests or fixtures. `attest capture --check-redaction <fixture>` before committing a fixture.
- Run tests with `--attest-replay=strict --attest-egress=block --attest-strict` (Vitest: `ATTEST_REPLAY=strict ATTEST_EGRESS=block ATTEST_STRICT=1`) unless the Run commands already set them.
- `not_evaluated` is never a pass; report the reason.
- Fix the agent when a contract fails on the behavior it guards; change a contract only with the user's agreement.

## Gaps (optional)

- <command or check that could not run offline, and why>

## Assumptions (optional)

- <answer chosen without the user, and the file it came from>
````
