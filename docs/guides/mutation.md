# Mutation testing

A suite that stays green when the agent's behavior breaks is not protecting anything. `attest mutate` measures that: it builds behavior mutants from your captured fixtures, runs your contracts against each one at no model cost, and reports a mutation score, the mutants nobody noticed, and a contract that would notice each.

Only behavior is mutated, never source code. A mutant is a copy of a fixture's trace or world snapshot with one deliberate change.

```bash
uv run attest mutate tests/attest/fixtures/*.attest.json --contracts .attest/contracts
```

## Operators

Mutants are enumerated in this fixed order, so the same fixtures give the same mutants.

| Operator | What it does | Suggested contract |
|---|---|---|
| `drop_tool_call` | Removes one tool call | `required_tools`, or `tool_cardinality` with a minimum when the tool is called more than once |
| `duplicate_tool_call` | Repeats one tool call right after itself | `tool_cardinality` on that tool per its business key (the first argument called `id` or ending in `_id`) |
| `reorder_tool_calls` | Swaps two neighbouring tool calls | `tool_order` |
| `perturb_argument` | Changes one scalar argument: a string gets a suffix, a number doubles, a bool flips | `arg_predicate` pinning the argument to the captured values |
| `inject_tool_error` | Marks a tool call failed with an error message (trace-v2 fixtures) | `failure_streak` with `max: 0` |
| `alter_world_effect` | In the fixture's world snapshot: duplicates a record, drops a record, or changes one field | an effect `idempotent` or `count` check |
| `truncate_output` | Cuts a string in the final output to its first half | a `content` `contains` check on the cut text |

A mutant identical to the original, or to an earlier mutant, is dropped. Each mutant has a stable id, `<fixture>:<operator>:<site>`, and a description such as `duplicate issue_refund (order_id=O-1042)`.

`--max-mutants N` keeps a sample of N mutants, chosen with a PCG seeded by `--seed` (default 0), in enumeration order. Without a cap every mutant is kept and the seed has no effect. The mutant set depends only on the fixtures, the seed and the cap.

## Give mutate your contracts

A suite's contracts are test code, and a report row records a result, not the spec. So the engine records the specs as it evaluates them. Set `ATTEST_CONTRACTS_DIR` for the test run:

```bash
ATTEST_CONTRACTS_DIR=.attest/contracts uv run pytest
ATTEST_CONTRACTS_DIR=.attest/contracts npx vitest run
```

Each engine process writes one file, `contracts-<pid>-<random>.ndjson`, to that directory (created `0700` if it does not exist, files `0600`; an existing directory keeps its mode). Every line is `{test_id, file, line, assertion: {assertion_id, type, spec}}`; the pytest plugin and the Vitest integration send the test id, file and line with each batch. Traces and model outputs are not recorded. An effect spec keeps the world snapshot it embeds. Recording is off unless the variable is set; delete the directory to start over.

Without `--contracts`, mutate uses the contracts drafted into each fixture by `attest capture`.

## What counts as killed

Only deterministic specs run: trace, schema, constraint, content, trace-tree and effect. Judge, embedding and plugin specs are skipped and counted in `skipped_specs`. No model is called.

A spec counts for a fixture only if it passes on the unmutated fixture. A spec recorded from another test's trace drops out; an effect spec applies when its world has the same `definition_sha256` as the fixture's, and is then evaluated on the fixture's snapshot in place of the one it embeds. A mutant is **killed** when any applicable spec hard-fails on it (a `soft` spec does not kill) and **survived** when none does. A mutant of a fixture with no applicable spec is `no_applicable_specs` and is left out of the score.

```text
score = killed / (killed + survived)
```

The score is `null` when no mutant could be scored. When that is because no contract applies to any fixture, the command also exits 1; the report is still printed.

## Read the report

```text
mutation score: 62% (13 killed, 8 survived)
...
survivors:
  inc-1042:duplicate_tool_call:3
    duplicate issue_refund (order_id=O-1042)
    kill with: trace {"check":"tool_cardinality","max":1,"per":"order_id","soft":false,"tool":"issue_refund"}
    (issue_refund is called at most 1× per order_id)
```

`--json` prints [`cli/mutate.schema.json`](../../proto/attest/v1/cli/mutate.schema.json): `score`, `seed`, `fixtures`, `mutants` (`id`, `fixture`, `operator`, `description`, `status`, `killed_by` with the assertion id, test id and spec), `survivors_suggestions`, `skipped_specs` and `applicable_specs`.

Each suggested contract was checked before it was reported: it passes on the unmutated fixture and hard-fails on the survivor. An effect suggestion leaves out the world snapshot; the test supplies it through the world helper. A survivor the operator table cannot express has no suggestion.

## Example: the double refund

A fixture captured from the fixed refund agent has one `issue_refund` for `O-1042`. A suite that only reads the reply (`output_contains("refund of $84.00")`) lets the `duplicate issue_refund` mutant survive: the reply is the same whether the customer was refunded once or twice. Add the suggested `tool_cardinality` contract, or an effect `count` on the `refunds` resource, and the mutant is killed.

## Gate on the score

```yaml
# attest.policy.yaml
min_mutation_score: { score: 0.8, severity: warn }
```

```bash
uv run attest mutate tests/attest/fixtures/*.attest.json --contracts .attest/contracts --json > mutation.json
uv run attest policy evaluate --policy attest.policy.yaml --report attest-report.json --mutation mutation.json
```

See [policies](policies.md#mutation-score). Exit codes: 0 a report was produced, 1 no contract applies to any fixture, 2 usage or load error.
