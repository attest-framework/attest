# Changelog

All notable changes to Attest are documented in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/). One `VERSION` covers the engine, the Python SDK (`attest-ai`) and the TypeScript SDKs (`@attest-ai/core`, `@attest-ai/vitest`); PyPI shows pre-releases in PEP 440 form (`1.0.0-rc.1` → `1.0.0rc1`).

## [Unreleased]

### Added

- `attest capture --name <name> -- <command>` turns a run into a regression fixture. The engine starts a loopback listener hosting a recording proxy for OpenAI Chat Completions, OpenAI Responses and Anthropic Messages (streaming and not) and an OTLP/HTTP trace receiver (`http/protobuf` and `http/json`, optionally gzip), runs the command with `OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL` and `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` pointing at it, forwards model calls to the upstreams set by those variables in the capture's own environment (default `https://api.openai.com/v1` and `https://api.anthropic.com`), and writes `tests/attest/fixtures/<name>.attest.json` (`--out-dir` to change). Request headers are never recorded; values of environment variables whose names end in `KEY`, `TOKEN`, `SECRET` or `PASSWORD` and key-shaped strings (`sk-…`, `AKIA…`, `ghp_…`, bearer credentials, private keys) are replaced by `[redacted]` in keys and values. Trace and span ids, provider response and tool-call ids, timestamps, durations and emails are templated to stable placeholders across the trace, the exchanges and their replay keys, so capturing the same scripted run twice writes the same bytes. `--from <otlp.json>` builds a trace-only fixture (not replay-capable) from an export; `--serve` hosts the listeners for in-process helpers. `attest capture --check-redaction <fixture>` exits 1, naming the JSON path, when a fixture holds a secret.
- Capture drafts deterministic contracts from the run (the tools called, the order of their first calls, at most one call per `id` / `*_id` argument for each tool, the structured output's shape, and a cost ceiling at 1.5× the observed cost) and marks the fixture verified only when they all pass and the captured trace passes trace check; otherwise it exits 1 naming the failing contracts. `--expect-fail` saves an incident reproduction: verified when drafted contracts fail, which are recorded as expected failures.
- Fixture schema v1 (`proto/attest/v1/fixture-v1.schema.json`, fixtures in `protocol-tests/fixture-v1/`): the trace-v2 trace, the exchanges with a replay key (model, parameters, prompt hash, tool-schema hash), the drafted contracts, their verification and a `world` section reserved for simulated-world state. `attest trace check` accepts a fixture.

### Security

- The capture listener binds `127.0.0.1` on an ephemeral port, serves only the provider prefixes and `/v1/traces`, rejects requests whose `Host` is not a loopback name, bounds request and recorded response bodies at 32 MiB and OTLP input at the trace converter's limits, and closes when the command exits. Provider credentials pass through to the upstream in memory only.

## [1.0.0-rc.2] — 2026-11-02

Second release candidate, themed "Evidence": check a trace's integrity, write behavior contracts over what the agent did, get statistical verdicts and judge budgets, and judge with OpenAI, Anthropic, Gemini or a local ACP agent.

### Added

- Native Anthropic (Messages API) and Gemini (`generateContent`) judges: `ATTEST_JUDGE_PROVIDER=anthropic` or `gemini`, with keys from `ATTEST_JUDGE_API_KEY`, `ANTHROPIC_API_KEY`, or `GOOGLE_API_KEY` then `GEMINI_API_KEY`, `ATTEST_JUDGE_MODEL` required, and `ATTEST_JUDGE_BASE_URL` for gateways. Each provider has its own rate limit (`ATTEST_RATE_LIMIT_ANTHROPIC[_BURST]`, `ATTEST_RATE_LIMIT_GEMINI[_BURST]`); an outage is `not_evaluated: provider_unavailable`. `doctor` reports the new providers and key sources. See `docs/guides/judges.md`.
- Judge results record the provider in `judge_metadata.provider` next to `model`; reports print `judge: <provider>/<model>`.
- `attest calibrate --persist` stores labels keyed by judge provider and model (`--judge-provider`, `--judge-model`), so calibration is never pooled across judges.
- `ATTEST_JUDGE_PROVIDER=acp` runs judges locally through an Agent Client Protocol agent started by `ATTEST_JUDGE_ACP_COMMAND` (for example a Claude Code or Codex ACP adapter) on the developer's own subscription. The engine refuses it when `CI` or a CI vendor variable such as `GITHUB_ACTIONS` is defined. The agent runs in an empty temporary directory and its own process group, gets no file-system or terminal capability, and has every permission request rejected; it is not sandboxed, and the engine warns about this at startup. Every call, including simulated-user turns from `generate_user_message`, is bounded by `ATTEST_JUDGE_TIMEOUT_S`. The agent's environment loses provider keys and endpoint switches (`OPENAI_API_KEY`, `OPENAI_BASE_URL`, `CODEX_API_KEY`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`, `GEMINI_API_KEY`, `GOOGLE_API_KEY`, `ATTEST_JUDGE_*`), and logs name only the agent executable. Rate limit: `ATTEST_RATE_LIMIT_ACP[_BURST]`. See `docs/guides/judges.md`.
- `attest trace check <otlp.json>` reports the integrity of an OpenTelemetry trace export (orphan spans, parent cycles, duplicate span ids, unpaired tool calls and results, each with its span id) and its evidence coverage (model spans without tokens, cost, message content or model; tool spans without call id or content), as text or `--json`, exiting 1 on integrity errors. `attest trace convert <otlp.json>` turns the export into an Attest trace. Conversion from OTLP now runs in the engine on the current OpenTelemetry GenAI semantic conventions; malformed input is rejected with an error naming the span. See `docs/guides/trace-check.md`.
- Model-call budgets enforced by reservation: `ATTEST_MAX_JUDGE_CALLS` caps judge requests and `ATTEST_BUDGET_USD` caps the spend of judge and embedding calls per engine session; `evaluate_batch` accepts a per-batch `budget` (`max_judge_calls`, `max_cost_usd`). Every judge call, `repeat` run, bias probe, simulated-user message, retry and paid embedding call reserves one call and its worst-case cost (price × prompt bytes + price × output-token limit) before it is sent and is reconciled to the reported token usage, so concurrent assertions cannot overspend (a failed call keeps its reservation; a request without an output limit is capped at 1024 tokens); a call that cannot reserve is not sent and its assertion is `not_evaluated: budget_exhausted`. Prices come from a built-in table (`gpt-4.1`, `gpt-4.1-mini`, `gpt-4.1-nano`, `text-embedding-3-small`, `text-embedding-3-large`) or `ATTEST_JUDGE_PRICE_IN` / `ATTEST_JUDGE_PRICE_OUT` (USD per 1M tokens); a USD budget with an unpriced judge or embedding model, or with the `acp` judge, is a startup error. See `docs/guides/configuration.md` and `docs/guides/judges.md`.
- `evaluate_batch` results carry `usage`: model calls and the judge calls among them, input, output and cached tokens, reconciled `cost_usd`, `retry_cost_usd`, `unpriced_calls` and `budget_refused`. Report-v2 accepts it as `summary.usage`; the engine's markdown report prints `model calls · tokens (cached) · $ · retry cost`. Judge results cost the same per token on every provider (Anthropic and Gemini judges previously reported $0).
- Judges can abstain: built-in rubrics (now version `v2`) ask for `{"abstain": true, "reason": "..."}` when the output lacks the evidence the rubric needs, and the assertion is `not_evaluated: judge_abstained` with the reason. Only the structured field counts; no phrase in an explanation is matched. Abstentions are not cached, and abstaining `repeat` runs and bias probes are left out.
- `attest policy evaluate --baseline <tag>` with a `regression: {confidence, tolerance}` rule (defaults 0.95 and 0.05) compares the pass rate of each assertion with at least 2 runs in both the report and the baseline (fixed-N trials) with the baseline using a Wilson interval; single runs keep the `block_on_regression` status comparison, and trial assertions it cannot compare are `inconclusive` and returns `verdict` `pass`, `regressed` (exit 1) or `inconclusive` (exit 2) with per-assertion intervals. Report-v2 rows accept a 1-based `trial` and test rows a `trials` count. See `docs/guides/verdicts.md`.
- `attest baseline pin` stores each assertion's passes, evaluated runs and not-evaluated runs, folding the rows of fixed-N trials into one entry; `baseline show` prints them. The baselines table in `$ATTEST_CACHE_DIR/attest.db` gains the columns on first use, and rows pinned earlier count as one run.
- Fixed-N trials: `pytest --attest-trials=N` runs every test that uses the `attest` fixture N times, and the Vitest `trials()` helper (N from `n` or `ATTEST_TRIALS`) runs a test body N times. During a trial `evaluate()` records without enforcing; the test fails when the pass rate of its evaluated trials is below the minimum pass rate (default 1.0, so any failing trial fails it; `--attest-min-pass-rate` / `@pytest.mark.attest_min_pass_rate(...)`, Vitest `minPassRate` on `attestGlobalSetup` or `trials()`, or `ATTEST_MIN_PASS_RATE`), not-evaluated trials are left out of the rate, and a trial that raises counts as a failed trial whose rows are replaced by one `hard_fail` row per assertion. Report rows carry `trial` and test rows `trials`; `attest policy evaluate --baseline <tag>` with a `regression` rule turns them into `pass`, `regressed` or `inconclusive`. Assertion ids inside a test are derived from the test id and the assertion's position, so they match across trials and runs. See `docs/guides/statistical-verdicts.md`.
- Per-test budgets: `@pytest.mark.attest_budget(max_judge_calls=..., max_cost_usd=...)` and the Vitest `testBudget: { maxJudgeCalls, maxCostUsd }` option of `evaluate`/`inspect` (`budget` on `trials()`) send the test's remaining budget with each evaluation; agent spend from `metadata.cost_usd` is checked between trials only, and further trials stop once the test budget is spent.
- The pytest and Vitest run summaries print `attest usage: <calls> model calls · <tokens> tokens (<cached> cached) · $<cost> · retry cost $<cost>`, and reports record it in `summary.usage`. The SDKs know the `budget_exhausted` and `judge_abstained` reasons.
- Trace schema version 2 (`proto/attest/v1/trace-v2.schema.json`, fixtures in `protocol-tests/trace-v2/`): optional conversation `turns`, agent `handoffs`, `conversation_id`, and per-step `retry_of`, `error`, recorded request/response `exchange`, `span_id`, `parent_span_id` and `tool_call_id`. The engine accepts schema versions 0–2; version 1 traces are unchanged.
- Behavior-contract trace checks in the engine (Layer 3, `type: "trace"`): `tool_cardinality` (calls per tool, optionally per argument value, reported as `issue_refund called 2× for order_id=O-1042 (max 1)`), `trajectory` (`strict`, `unordered`, `subset`, `superset`, `partial_order`), `tool_order`, `cycle` (period 1–4), `failure_streak` and `arg_predicate`. They match tool calls only and count retried calls. Specs are listed in `proto/attest/v1/protocol-spec.md`.
- Behavior-contract DSL in Python and TypeScript: `tool_cardinality` / `toolCardinality`, `trajectory`, `trajectory_partial_order` / `trajectoryPartialOrder`, `tool_called_before` / `toolCalledBefore`, `no_tool_cycles` / `noToolCycles`, `max_tool_failure_streak` / `maxToolFailureStreak` and `tool_arg_satisfies` / `toolArgSatisfies`. Both SDKs emit identical specs, checked by `protocol-tests/contracts/dsl.json`. See `docs/guides/behavior-contracts.md`.

### Changed

- The `loop_detection` trace check shares the `tool_cardinality` counter; its failure explanation now reads `fetch called 3× (max 2)`. Its result status is unchanged.
- **BREAKING:** `attest calibrate --persist` requires `--judge-provider` and `--judge-model`. The calibration table in `$ATTEST_CACHE_DIR/attest.db` gains `judge_provider` and `judge_model` columns on first use; labels stored before have neither and are not counted for any provider.
- **BREAKING:** The Python `OTelAdapter` converts spans through the engine (`attest-engine trace convert`) instead of its own mapping, so it needs an engine and returns trace v2. Tool steps take `args` and `result` from `gen_ai.tool.call.arguments` / `gen_ai.tool.call.result` (previously `call_id`, `parameters` and `output` from non-standard attributes; the call id is now `tool_call_id`), a top-level `invoke_agent` span becomes a conversation turn instead of an `agent_call` step, and malformed spans raise `attest.exceptions.OTLPConversionError`. An engine binary is required (the SDK downloads its engine version, or set `ATTEST_ENGINE_PATH` to one built with `make engine`). `OTelAdapter.from_otlp()` converts an OTLP/JSON export given as a `pathlib.Path`, JSON text, bytes or a parsed mapping. Span events, span kind and resource attributes are not read. `to_otel_spans()` writes the current semantic convention attributes under an `invoke_agent` root span. `Trace.to_dict()` omits `parent_trace_id` when it is unset.
- **BREAKING:** The TypeScript `OTelAdapter.fromSpans()` converts spans through the engine and returns a `Promise<Trace>` (trace v2) instead of a `Trace`. Spans need `spanContext().spanId`; `parentSpanId` (sdk-trace-base 1.x) or `parentSpanContext` (2.x) and `status` are read. `OTelAdapter.fromOtlp()` converts an OTLP/JSON export, and malformed input rejects with `OTLPConversionError`. Invoke-agent, retrieval and `execute_tool` spans, previously dropped or misread, now convert as in Python. `new OTelAdapter(agentId)` is removed (use the static methods), span `attributes` must be OpenTelemetry attribute values, and `fromSpans([])` rejects (an export needs at least one span). The `Step` and `Trace` types gain the trace v2 fields.
- Built-in judge rubrics are version `v2` (abstain instruction added), so calibration labels stored against `v1` no longer match them.
- `repeat: N` judges take the mean of the two middle scores for an even number of scored runs (previously the upper middle score).
- Gemini judge usage counts thinking tokens (`thoughtsTokenCount`) as output tokens, as they are billed.
- Judge and embedding calls stop when the batch's context is cancelled.
- `attest policy evaluate --baseline` folds the rows of fixed-N trials into one row per assertion (worst status, mean score, summed cost and duration) before computing transitions.
- Judge cache entries are keyed by the whole judge prompt (rubric version, criteria and agent output) instead of the agent output alone. Two `passes_judge` assertions with different criteria on one output previously shared one cached score. Entries cached by earlier versions are misses and are judged again.
- Cached judge scores are scoped by judge provider as well as endpoint, rubric and model, so two providers behind the same gateway with the same model name never share a score. Entries cached by earlier versions are misses and are judged again.
- Judge provider error messages have any echoed API key replaced by `[redacted]` and control and Unicode format characters (bidi overrides, zero-width) removed before they reach results and reports.
- A constraint on a trace field the trace does not record, such as `cost_under` without `metadata.cost_usd`, explains its `not_evaluated: missing_evidence` result as `metadata.cost_usd absent`.
- The documentation website moved to `https://attest-framework.github.io/` (repository `attest-framework/attest-framework.github.io`, formerly `attest-website`). The release and SDK CI website dispatches target the renamed repository, and `https://attest-framework.github.io/attest/` redirects to the new root. The old `/attest-website/` path no longer resolves.

### Removed

- The MkDocs site configuration (`mkdocs.yml`) and the pages it built outside `docs/guides/`. `https://attest-framework.github.io/attest/` already redirects to the documentation website, which syncs `docs/guides/*.md` at each release.

### Fixed

- Install instructions in the README, `docs/guides/merge-gate.md`, `docs/guides/framework-adapters.md` and the Python SDK README (the PyPI page) use `uv add "attest-ai>=1.0.0rc1"` instead of `pip install`, and `npm install -D "@attest-ai/core@>=1.0.0-rc.1" "@attest-ai/vitest@>=1.0.0-rc.1"`. A bare `pip install attest-ai`, `uv add attest-ai` or `npm install @attest-ai/core` installs 0.9.0, the newest stable release, until 1.0.0 is published.

## [1.0.0-rc.1] — 2026-11-02

First release candidate, themed "Trust": a red result means a real failure, the SDKs run only an engine of their own version, and `attest init` goes from an empty project to a CI merge gate without an API key.

### Added

- `ATTEST_SIMULATION=1` runs the engine with providers disabled: layers 1–4 evaluate for real, layers 5–6 report `not_evaluated: simulation`.
- `inspect()` (Python `attest.inspect()`, `@attest-ai/vitest` `inspect()`) evaluates without enforcing. Python `attest.enforce(verdict)` applies the session's strict setting to an `AgentResult`, `RepeatResult` or `ScenarioResult`: it fails with the failing trials' diagnostics or skips with the not-evaluated reasons summed across trials. `--attest-strict` (pytest), `strict: true` (Vitest) and `ATTEST_STRICT=1` turn not-evaluated skips into failures.
- `attest init` (Python and TypeScript) writes a sample test that passes with no API key: a refund agent's run built with `TraceBuilder`, checked by schema, constraint, trace and content assertions, with one line marked `ATTEST-DEMO` that turns it red with the layer, expected and actual value. It also writes `attest.policy.yaml` (block on hard failures, warn on not-evaluated assertions). The Python sample test defines the `my_agent_result` fixture it uses (the old sample used it without defining it), so an existing `tests/conftest.py` does not affect it.
- `attest init --ci github` writes `.github/workflows/attest.yml`: the test step continues on failure, the report upload and `attest policy evaluate` always run, the `pr-annotate` action always runs for pull requests from the same repository (a fork's read-only token cannot post reviews), a policy warning (exit 2) does not fail the job, and a final step fails the job when the tests failed. See `docs/guides/merge-gate.md`.
- report-v2 (`proto/attest/v1/report-v2.schema.json`, fixtures in `protocol-tests/report-v2/` checked in Go, Python and TypeScript): every assertion row carries the `test_id`, `file` and `line` of its test, a `tests` list gives each test's outcome (`passed`, `failed`, `skipped`) and not-evaluated reasons, and `summary.not_evaluated` counts not-evaluated rows.
- `pytest --attest-report=<path>` writes report-v2 for every test in the session.
- `AttestReporter` (`@attest-ai/vitest/reporter`) writes report-v2 when given `outputFile` in the Vitest config, or from the command line: `vitest run --reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=<path>`. `inspect()`/`evaluate()` attach results to the running test, so they reach the reporter from Vitest workers; a retried test reports only its last attempt's results (pytest reruns likewise).
- `release.yml` publishes a release from one tag push: it builds the six engine binaries, checksums, the Python distributions and the npm tarballs once, verifies them, and after `release` environment approval publishes the GitHub (pre-)release, then PyPI (trusted publishing) and npm (trusted publishing with provenance) from those stored artifacts, records which components published in the release notes, and dispatches the website sync. A tag that differs from `VERSION` fails before anything builds. `workflow_dispatch` with `dry_run: true` runs the whole pipeline without publishing.
- `setup-attest` accepts a `ref` input: consumer repositories build the engine and install the Python and TypeScript SDKs from any attest branch, tag or commit.
- `CHANGELOG.md` is the single changelog; `scripts/release-notes.sh <version>` prints one version's section.
- `attest-engine doctor [--json]` reports the engine and protocol versions, the capabilities `initialize` would advertise, the judge provider, model, base URL and key source (never the key), the embedding provider, simulation mode and the cache directory. JSON follows `proto/attest/v1/doctor.schema.json`. A configuration error exits 1 with the server's startup message; a plain-http judge endpoint on a non-loopback host is flagged as insecure.
- `ATTEST_JUDGE_BASE_URL` points the LLM judge at any OpenAI-compatible endpoint. `ATTEST_JUDGE_API_KEY` overrides `OPENAI_API_KEY` for the judge. See `docs/guides/configuration.md`.
- `attest-engine --help` lists every command; each command is one file registered through a command registry.
- `attest` console script for the Python SDK (`uv run attest`), matching `npx attest`. Both implement `attest init` and forward every other command (`doctor`, `baseline`, `policy`, `cache`, `calibrate`, `version`) to a version-checked engine with arguments, output and exit code unchanged. A forwarded command must be listed by the engine's `help`; an engine that predates it (such as the `v1.0.0-rc` prerelease, which reports the same version but has no command registry) exits 1 before running. `attest --help` and bare `attest` always exit 0 and never download: they list `init`, plus the engine's commands when a matching engine is already available through `ATTEST_ENGINE_PATH`, `PATH` or the versioned cache, and otherwise print how to get one.
- `EngineVersionMismatchError` (Python `attest.exceptions`, TypeScript `engine-manager`) names the engine path, where discovery found it, the version it reports and the version required.

### Changed

- **BREAKING:** the engine reports `not_evaluated` with a reason (`provider_unavailable`, `provider_not_configured`, `simulation`, `missing_evidence`, `short_circuited`) instead of failing or dropping assertions it cannot judge. Judge and embedding outages and missing keys no longer show as `broken_code`.
- **BREAKING:** `attest-engine policy evaluate` warns (exit 2) on `not_evaluated` assertions; set `not_evaluated: block` in the policy to block instead.
- **BREAKING (Python):** `attest.evaluate()` fails the test on any hard failure and skips it with `not evaluated: <reason>` when an assertion could not be evaluated.
- **BREAKING (TypeScript):** `@attest-ai/vitest` `evaluate()` fails the test on any hard failure and skips it with `not evaluated: <reason>` when an assertion could not be evaluated.
- **BREAKING:** simulation mode in both SDKs runs the real engine with providers disabled instead of returning fabricated passes; TypeScript `simulationEvaluateBatch` is removed. `config(simulation=True)` / `config({ simulation: true })` requires an engine that advertises `simulation_mode`, so it can never reach a real provider.
- Python diagnostics count failures, not rows, and list not-evaluated assertions with their reason.
- The `pr-annotate` action places each failed assertion at the `file` and `line` its report-v2 row carries (the test that requested it): an inline review comment when that line is in the pull request diff, an entry with `file:line` in the review body otherwise, and an `::error` (hard failure) or `::warning` (soft failure) workflow annotation at that file and line either way. It no longer guesses line 1 from test ids in the explanation; rows without a location are listed in the review body. Its unit tests run in GitHub Config CI. When the `policy` input is set, a policy evaluator exit code other than 0, 1 or 2 (for example 127, `engine-binary` not found) now fails the step with an error naming the code; it previously counted as a pass.
- **BREAKING:** `@attest-ai/vitest` requires Vitest 3 or later (`AttestReporter` uses the Vitest 3 reporter API), and its `@attest-ai/core` peer range is `>=1.0.0-rc`; the old `>=0.3.0` range excluded 1.0.0 prereleases, so `npm install` of the two packages failed.
- **BREAKING:** under `test.concurrent`, `inspect()` and `evaluate()` require the test context (`{ context }`) and throw without it, so results are never attached to another test.
- The TypeScript sample test written by `attest init` is `tests/my-agent.test.ts` (was `tests/test_my_agent.test.ts`).
- The README quickstart runs as written: install, `attest init`, `pytest` / `vitest run`, the marked edit, the report and the policy gate. A test runs every README code block.
- **BREAKING:** `ATTEST_BUDGET_MAX_COST` is renamed `ATTEST_MAX_SOFT_FAILURES`; it always counted soft failures in one engine session, not cost. No alias.
- **BREAKING:** with `ATTEST_JUDGE_BASE_URL` set, only `ATTEST_JUDGE_API_KEY` is sent to the endpoint; `OPENAI_API_KEY` is used only at the default OpenAI endpoint. A base URL without `ATTEST_JUDGE_API_KEY`, with credentials, a query or a fragment, is a startup error.
- **BREAKING:** `OPENAI_API_KEY` in the engine's environment now enables the OpenAI judge and OpenAI embeddings (previously only `ATTEST_OPENAI_API_KEY` did).
- **BREAKING:** non-integer `ATTEST_EMBEDDING_CACHE_MAX_MB`, `ATTEST_JUDGE_CACHE_MAX_MB`, `ATTEST_HISTORY_MAX_ROWS` and `ATTEST_HISTORY_MAX_AGE_DAYS` now fail startup even when no provider is configured, and `ATTEST_RATE_LIMIT_OPENAI[_BURST]` values <= 0 fail startup instead of falling back to defaults.
- **BREAKING:** an unknown `ATTEST_EMBEDDING_PROVIDER` (anything other than `auto`, `openai`, `onnx`) is rejected at startup.
- **BREAKING:** `attest-engine <unknown command>` exits 2 with usage instead of starting the JSON-RPC server.
- Judge cache entries are scoped to the judge endpoint, so two endpoints serving the same model name no longer share scores; entries cached by earlier versions become misses.
- **BREAKING:** the SDKs cache the engine at `~/.attest/bin/<ENGINE_VERSION>/attest-engine` (canonical `1.0.0-rc.N`, never the PEP 440 form) without a `.engine-version` marker. The legacy `~/.attest/bin/attest-engine` is no longer read and is left in place.
- **BREAKING:** one discovery function per SDK (`attest.engine_manager.resolve_engine`, TypeScript `resolveEngine`) serves the pytest fixture, Vitest, `EngineManager` and the `attest` CLI: explicit path → `ATTEST_ENGINE_PATH` → `PATH` → versioned cache → download. The engine found must report exactly `ENGINE_VERSION`, and `initialize` must report it too; otherwise the SDK raises a version-mismatch error instead of running. A mismatched engine on `PATH` is not skipped. The monorepo `bin/` and `./bin/` fallbacks are gone; set `ATTEST_ENGINE_PATH` to a locally built engine.
- **BREAKING:** `attest policy evaluate` and every other forwarded command exit 1 before running when no matching engine is available, instead of running whatever `attest-engine` was found.
- **BREAKING:** `attest cache` and `attest calibrate` run in the engine (`attest-engine cache`, `attest-engine calibrate`); their output follows the engine's format.
- The TypeScript engine download uses the `.exe` asset name on Windows, matching the release assets.
- `PATH` discovery in the Python SDK checks only the platform binary name in each `PATH` entry, skipping empty entries, so the current directory and other `PATHEXT` extensions are never searched. Forwarded commands killed by a signal exit 128 + N in both SDKs.

### Removed

- `docs/changelog.md`, superseded by this file.
- **BREAKING:** `ATTEST_OPENAI_API_KEY`; use `ATTEST_JUDGE_API_KEY` or `OPENAI_API_KEY`.
- **BREAKING:** `ATTEST_JUDGE_RPM` and `ATTEST_JUDGE_BURST`; use `ATTEST_RATE_LIMIT_OPENAI` and `ATTEST_RATE_LIMIT_OPENAI_BURST`.
- **BREAKING:** the deprecated Python adapter Protocols `attest.adapters.TraceAdapter`, `ProviderAdapter` and `FrameworkAdapter`; subclass `BaseAdapter` or `BaseProviderAdapter`. `Agent(adapter=...)` takes a `BaseAdapter`.
- **BREAKING:** `attest validate` (Python and TypeScript) and the Python `attest run` / bare `attest` pytest pass-through; run `pytest` or `vitest` directly. `attest` with no arguments prints help.

### Fixed

- Vitest workers start their own engine when the `attestGlobalSetup()` client is not visible to them.

### Security

- The ONNX embedding model downloads from a pinned Hugging Face revision and is rejected unless its sha256 matches; a cached model that does not match is replaced. Downloads use a unique temp file, a timeout and a 256 MiB cap, and are published 0644.
- ONNX Runtime guidance targets 1.26.0 (required by `onnxruntime_go` v1.31) with published checksums; `ATTEST_ONNX_RUNTIME_LIB` is honored and resolved to an absolute path.
- Judge base URL errors never echo the URL, so credentials in it cannot reach logs.

## [1.0.0-rc] — 2026-04-30

Tagged, not published to registries. Engine binaries are attached to the GitHub pre-release.

### Added

- `FailureClass` taxonomy on `AssertionResult` (`failure_class`), mirrored in both SDKs.
- `BaselineStore` for named result snapshots; reports carry a `baseline` delta block and surface the failure class.
- CI workflow commands: `attest-engine baseline pin/list/show/delete` snapshots reference runs; `attest-engine policy evaluate` reads a merge-blocking policy file and returns tiered exit codes (0 pass, 1 block, 2 warn); the Python SDK ships `attest baseline` / `attest policy` shims; the `.github/actions/pr-annotate` composite action posts inline review comments per failed assertion.

### Known limitations

- The TypeScript admin CLI shim (`attest baseline`, `attest policy`) is not available; the Python SDK ships them.

## [0.9.0] — 2026-04-30

Published: PyPI `attest-ai==0.9.0`, npm `@attest-ai/core@0.9.0` and `@attest-ai/vitest@0.9.0`, engine binaries on the GitHub release.

### Added

- Calibrated judge subsystem: required `Rubric.Version` (built-ins pinned to v1); calibration history keyed by rubric name, version and prompt hash.
- `repeat: N` judge runs (1–16 samples) returning the median with mean and standard deviation; cost reported as N × per-run. `meta_eval` remains an alias for `repeat: 3`.
- Bias probes (`verbosity`, `position`, `self_preference`, or `all`) that report deltas from controlled mutations of the agent output.
- Calibration sets: CSV and JSONL human-label loaders and `attest-engine calibrate` / `attest calibrate` (Python and TypeScript) producing identical Cohen's κ, agreement and ROC-AUC.
- SQLite-backed `CalibrationStore` alongside the assertion-history store.
- OpenTelemetry GenAI parity: current span names (`chat`, `text_completion`, `embeddings`, `invoke_agent`, `execute_tool`, retrieval) with attribute-driven dispatch; `gen_ai.agent.name` / `gen_ai.agent.id` drive `TraceTree` parent/child resolution; `TraceBuilder.export_otel()` emits OTLP-compatible spans.

### Changed

- Custom rubrics registered without `Version` now fail at registration time.

## [0.8.0] — 2026-04-30

Tagged, not published to registries. Engine binaries are attached to the GitHub release.

### Added

- Hierarchical Markdown report: summary header (counts, cost, P50/P95 latency), per-assertion failure blocks and a per-layer breakdown.
- JSON report v2 (`report_version: 2`) with per-layer indices, latency percentiles and a `simulated` flag. The v1 emitter remains available through `ReportOptions{Version: ReportVersionV1}`.
- Diagnostic fields on `AssertionResult` (`Layer`, `Type`, `TraceNodePath`, `Expected`, `Actual`, `SuggestedAction`) and `JudgeMetadata` (model, rubric, prompt hash, sample scores, mean, standard deviation, high-variance flag), mirrored in both SDKs.
- Python: `attest.cli.diagnostics` renderers, `AgentResult.render_diagnostics()`, automatic diagnostic output in the pytest terminal summary and the `--attest-diagnostics` flag.
- TypeScript: `diagnostics.ts` renderers; `AttestCostReporter` renders diagnostics for failed runs.

## [0.7.0] — 2026-04-30

Tagged, not published to registries. No GitHub release was created.

### Added

- Protocol diagnostic ring buffer and desync rejection in the Python and TypeScript SDKs.
- Cross-SDK protocol conformance fixture suite under `protocol-tests/`.
- Per-provider rate-limit configuration with a typed rate-limit error.
- Results surface dynamic threshold degradation.

### Changed

- Bounded L5/L6 evaluation, strict environment-variable parsing and a simulation marker on results.
- Listener exceptions are isolated from the protocol reader.

## Earlier releases

Versions 0.1.0 through 0.6.0 are described in their [GitHub releases](https://github.com/attest-framework/attest/releases); 0.6.1 exists only as a tag. 0.1.0–0.6.0 were published to PyPI; 0.3.0–0.6.0 were also published to npm.

[Unreleased]: https://github.com/attest-framework/attest/compare/v1.0.0-rc.2...HEAD
[1.0.0-rc.2]: https://github.com/attest-framework/attest/releases/tag/v1.0.0-rc.2
[1.0.0-rc.1]: https://github.com/attest-framework/attest/releases/tag/v1.0.0-rc.1
[1.0.0-rc]: https://github.com/attest-framework/attest/releases/tag/v1.0.0-rc
[0.9.0]: https://github.com/attest-framework/attest/releases/tag/v0.9.0
[0.8.0]: https://github.com/attest-framework/attest/releases/tag/v0.8.0
[0.7.0]: https://github.com/attest-framework/attest/compare/v0.6.1...v0.7.0
