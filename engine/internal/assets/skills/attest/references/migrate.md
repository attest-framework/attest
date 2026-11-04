# Migrate from Promptfoo or DeepEval

Goal: every existing eval case has an attest equivalent, a recorded decision, or an explicit "not migrated" entry, and the attest suite runs in CI with zero model calls. Track all of it in `MIGRATION.md` at the user's repo root. Background: `attest guide behavior-contracts`, `attest guide capture`, `attest guide replay`, `attest guide judges`, `attest guide verdicts`.

## Principles

- Promptfoo and DeepEval call the model on every run and mostly grade text. Attest records a run once (`attest capture`) and replays it, and asserts on behavior first: tool calls, side effects, structure, cost. Migrate each case to the most deterministic layer that still expresses its intent.
- An LLM-graded metric becomes a judge assertion (layer 6) only when no deterministic check carries the intent. In CI without a judge key it reports `not_evaluated: provider_not_configured`; record that in the decisions log instead of hiding it.
- Do not delete the old suite until the user approves the finished `MIGRATION.md`.

## Concept map

| Promptfoo | DeepEval | Attest |
| --- | --- | --- |
| `promptfooconfig.yaml` `tests[]` with `vars` | `LLMTestCase` / goldens in an `EvaluationDataset` | one pytest test (`@pytest.mark.parametrize`) or Vitest `it.each` per case set |
| `providers` (live model call per eval) | the app called live in each test | the agent's real code under replay: `attest_replay("<name>")` / `useReplay("<name>")`, fixture from `attest capture` |
| `assert[]` on the output | `metrics=[...]` in `assert_test` | an `expect(result)` / `attestExpect(result)` chain, enforced by `attest.evaluate(...)` / `evaluate(...)` |
| `npx promptfoo eval` | `deepeval test run test_x.py` | `uv run pytest` / `npx vitest run` |
| `defaultTest.assert` | shared metric list | a helper that returns the common chain |
| web viewer / Confident AI report | Confident AI dashboard | report-v2 (`--attest-report`), `attest report summary`, `attest policy evaluate` |
| test `threshold`, assertion `weight` | metric `threshold` | per-assertion `threshold` (judge, similarity) and `soft=True`; no weights: record the decision |

## Assertion map

| Promptfoo `type` | DeepEval metric | Attest Python / TypeScript | Layer |
| --- | --- | --- | --- |
| `contains`, `icontains` | | `output_contains(v, case_sensitive=True)` / `outputContains`; case-insensitive is the default | L4 |
| `not-contains`, `not-icontains` | | `output_not_contains(v)` / `outputNotContains` | L4 |
| `contains-all`, `contains-any` | | `output_has_all_keywords([...])`, `output_has_any_keyword([...])` / `outputHasAllKeywords`, `outputHasAnyKeyword` | L4 |
| `regex`, `starts-with`, `equals` | | `output_matches_regex(pattern)` / `outputMatchesRegex` (anchor and escape for `equals`, `starts-with`) | L4 |
| `is-json`, `contains-json` with a schema | `JsonCorrectnessMetric` | `output_matches_schema(schema)` / `outputMatchesSchema`; one field: `output_field_matches_schema` | L1 |
| `is-valid-openai-tools-call`, `is-valid-function-call` | | `tool_args_match_schema(tool, schema)` / `toolArgsMatchSchema` | L1 |
| custom `javascript` / `python` checking tool calls | `ToolCorrectnessMetric` (`tools_called` vs `expected_tools`) | `required_tools`, `trajectory(tools, mode=...)`, `tool_called_before`, `tool_cardinality`, `forbidden_tools`, `tool_arg_satisfies` | L3 |
| | `TaskCompletionMetric` | the effect the task must leave: `world(state).count(...)` (`write-contracts.md`), else `required_tools` | L3 |
| `cost` | | `cost_under(usd)` / `costUnder` (needs `metadata.cost_usd` in the trace) | L2 |
| `latency` | | `latency_under(ms)` / `latencyUnder` | L2 |
| | token limits | `tokens_under(n)` / `tokensUnder` | L2 |
| `similar` | | `output_similar_to(reference, threshold=0.8)` / `outputSimilarTo` (embeddings: a model call unless a local onnx engine; `attest guide configuration`) | L5 |
| `llm-rubric`, `g-eval`, `model-graded-closedqa`, `factuality`, `answer-relevance` | `GEval`, `AnswerRelevancyMetric`, `FaithfulnessMetric`, `HallucinationMetric`, `Contextual*Metric`, `SummarizationMetric`, `BiasMetric`, `ToxicityMetric` | `passes_judge(criteria, rubric="default", threshold=0.8)` / `passesJudge`; built-in rubrics `default`, `helpfulness`, `accuracy`, `safety` | L6 |
| other custom `javascript` / `python` | custom `BaseMetric` | the deterministic DSL method that matches, else a plain `assert` / `expect` in the test on the trace | test code |
| `levenshtein`, `rouge-n`, `bleu`, `perplexity` | | no equivalent: keep, replace with a contract, or drop; log the decision | |

DeepEval metrics where lower is better (`HallucinationMetric`, `BiasMetric`, `ToxicityMetric`) invert: write the judge criteria positively ("contains no claim absent from the context") with a minimum score.

## Procedure

1. **Inventory.** Find the suites: `git ls-files '*promptfooconfig*' '*.promptfoo*' 'promptfoo/*'` and `git grep -l -E 'from deepeval|import deepeval|assert_test\('`. Create `MIGRATION.md` from the template below with one row per test case (Promptfoo: each `tests[]` entry; DeepEval: each test function or golden).
2. **Install and check.** `uv add --dev "attest-ai>=1.0.0"` (Python) or `npm install -D "@attest-ai/core@>=1.0.0" "@attest-ai/vitest@>=1.0.0" "vitest@^4"`; then `attest doctor --json`.
3. **Make the agent capturable.** It must read `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` and export OTLP spans (`capture-incident.md`). A Promptfoo suite that tests a bare prompt (no agent) needs a small entry point that sends the prompt with the OpenAI or Anthropic SDK.
4. **Capture each case once** with its `vars` / `input`: `attest capture --name <case> -- <command>`. This calls the model: get the user's approval and say how many captures it takes. Group cases that share one run.
5. **Write the test per suite** with the assertion map, replaying each case's fixture. Run it with zero model calls (`write-contracts.md` step 4).
6. **Update `MIGRATION.md`** after every case: status, attest test id, layer, decision. Record each lossy mapping in the decisions log.
7. **Wire CI** (`ci-setup.md`). Run old and new suites side by side until the user signs off; then remove the old suite only on request.

## MIGRATION.md template

Write exactly this structure; update it after each step, not at the end.

```markdown
# Migration to attest

Source: <Promptfoo | DeepEval> (<config or test paths>)
Target: <pytest | Vitest>, tests in <dir>
Started: <YYYY-MM-DD> · Last updated: <YYYY-MM-DD>

## Progress

| Suite | Cases | Migrated | Judge-only | Not migrated |
| --- | --- | --- | --- | --- |
| <suite name / file> | <n> | <n> | <n> | <n> |

## Cases

### <suite name / file>

| Case | Source assertions | Attest assertions | Layer | Fixture | Status | Test |
| --- | --- | --- | --- | --- | --- | --- |
| <id or description> | <e.g. icontains "charge"; llm-rubric "polite"> | <e.g. output_contains("charge"); passes_judge("polite")> | <L4, L6> | <tests/attest/fixtures/x.attest.json> | <todo / captured / passing / not_evaluated: reason / not migrated> | <tests/test_x.py::test_case> |

## Decisions log

| Date | Case or suite | Decision | Reason |
| --- | --- | --- | --- |
| <YYYY-MM-DD> | <case> | <e.g. rouge-n dropped; replaced by tool_cardinality> | <why> |

## Open items

- [ ] <captures awaiting approval, judge cases without a provider in CI, old suite removal>

## Verification

- Command: `<uv run pytest ... | npx vitest run ...>`
- Result: <N passed · N failed · N not evaluated (reasons)> · `<N replayed · 0 live model calls>`
```

Status values: `todo`, `captured`, `passing`, `not_evaluated: <reason>`, `not migrated`. A case is `passing` only after its test ran green with zero live model calls.
