# Changelog

All notable changes to Attest are documented in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/). One `VERSION` covers the engine, the Python SDK (`attest-ai`) and the TypeScript SDKs (`@attest-ai/core`, `@attest-ai/vitest`); PyPI shows pre-releases in PEP 440 form (`1.0.0-rc.1` → `1.0.0rc1`).

## [Unreleased]

## [1.1.0] — 2026-11-11

Stable release themed "Hardened". 1.1.0 is 1.1.0-rc.5 unchanged, after three consecutive green nightly walkthroughs of every refund-desk incident on the released channel. The release candidates added each piece: fail-closed judges, stable assertion ids and failing unevaluated chains (rc.1), runner parity on Vitest 3 and 4 and safe capture defaults (rc.2), a SHA-pinned supply chain, least-privilege pull-request annotation and Vitest 5 support (rc.3), relational world-effect contracts, semantic refresh diffs, per-operator mutation scores and executable public claims (rc.4), checked-chain ownership and failed-test policy gates (rc.5). No changes since rc.5.

**Upgrade note (from 1.0.1):** Several changes turn a silent pass into a failure. Read the **Behavior change** lines in the rc.1 to rc.5 entries below before upgrading; the rc.5 upgrade note covers chain re-evaluation, unscoped concurrent lifecycle assertions on Vitest <4.1 and the default `tests_failed` policy rule.

## [1.1.0-rc.5] — 2026-11-07

Fifth release candidate of 1.1.0, themed "Every check ran". Checked-chain ownership and failed-test policy gates close silent-green paths in Python and Vitest, while packaged npm guides and provider-object handling improve onboarding and compatibility. Changes that alter results are marked **Behavior change** below.

**Upgrade note:** Chains are now enforced after append-after-evaluate, when fixture-built, and in isolated Vitest `it.concurrent` bodies: evaluate every chain again after adding assertions. Move unscoped concurrent lifecycle assertions into the test body or upgrade to Vitest >=4.1; cached, non-isolated concurrent files fail explicitly when guard hooks are unavailable. Policy evaluation now blocks failed native tests by default through `tests_failed`; explicitly configure `tests_failed: warn` or `tests_failed: off` only when a non-blocking verdict is intended.

### Changed

- **Behavior change:** policy evaluation now blocks reports containing failed test outcomes, even when every Attest assertion passed; configure `tests_failed: warn` or `tests_failed: off` to explicitly downgrade or disable this rule.
- **Behavior change:** policy evaluation exits 3 for report-v2 input with missing/null `tests` or an outcome outside `passed|failed|skipped`, instead of treating the malformed input as a passing verdict.

### Fixed

- Fix LlamaIndex trace construction for genuine OpenAI provider tool-call objects while retaining support for dictionary tool calls.
- Include installation instructions, a runnable Vitest example, and documentation links in the `@attest-ai/core` and `@attest-ai/vitest` npm packages.
- **Behavior change:** Python tests now fail with the existing `expect() chain built but never evaluated` diagnostic when assertions were added after the last evaluation.
- **Behavior change:** Assertion chains created during fixture setup or teardown are checked for evaluation and attributed to the item executing that fixture phase rather than silently passing or leaking into the next test.
- Preserve scheme-2 assertion ids across reevaluation while assigning new assertions the next occurrence index in each scope/trial.
- **Behavior change:** Isolated Vitest concurrent test bodies now fail when their own Attest chains were never evaluated; another test's evaluated chain cannot satisfy their guard. Unhooked concurrent chains in cached, non-isolated files fail explicitly instead of silently opting out.
- **Behavior change:** Adding an assertion after evaluation, including while an evaluation is in flight, leaves the chain unevaluated and fails teardown unless it is evaluated again.
- **Behavior change:** Unscoped concurrent lifecycle assertions on Vitest <4.1 fail with guidance instead of silently opting out. Move the chain into the body or upgrade Vitest.
- Preserve assertion ids across re-evaluation.
- **Behavior change:** Detect unevaluated mutations of chains shared across tests; untouched shared chains are not charged to another test.
- **Behavior change:** report Vitest suite/module `beforeAll` and `afterAll` errors as failures of their collected descendant tests instead of silently reporting them passed or skipped, so the `tests_failed` policy rule can block the run.

### Docs

- Explain chain re-evaluation, pytest fixture ownership and shared mutations, concurrent Vitest guard coverage and compatibility limits.
- Document failed-test policy gating, invalid-report errors, and Vitest suite/module hook failure reporting.

### Dependencies

- Python `google-genai` 1.63.0 → 1.75.0 (development/conformance only; the proposed 2.x update is incompatible with the framework test environment); user-facing optional ranges are unchanged. (#222)
- TypeScript `vitest` 4.1.12 → 5.1.0 (development only); CI retains Node 20 consumer coverage with an isolated Vitest 4 override and tests the committed Vitest 5 default on Node 22; published Node/Vitest peer ranges are unchanged. (#223)
- Python `openai` 1.83.0 → 2.54.0 (development/conformance only), with compatible CrewAI 1.10.1, Instructor 1.13.0 and LlamaIndex OpenAI 0.6.8 lockfile versions; user-facing optional ranges are unchanged. (#224)
- Python `ollama` 0.6.1 → 0.6.3 (development/conformance only); user-facing optional ranges are unchanged. (#225)
- Python `anthropic` 0.81.0 → 2.4.0 (development/conformance only), with development-lock OpenTelemetry security overrides; user-facing optional ranges are unchanged. (#226)
- TypeScript `@langchain/core` 1.1.40 → 1.3.3 (development/conformance only); user-facing optional peer ranges are unchanged. (#227)
- Python `langchain-core` 1.3.3 → 1.7.4 (development/conformance only); user-facing optional ranges are unchanged. (#228)
- GitHub Actions development/CI tooling: `actions/checkout` v7.0.1, `actions/setup-go` v7.0.0, `actions/setup-python` v7.1.0 and `actions/setup-node` v7.1.0 (major 7), `astral-sh/setup-uv` v10.3.3 (major 10), and `pnpm/action-setup` v6.1.1 (major 6); synchronize SHA-pinned generated Python/TypeScript workflows to checkout v7.0.1 and setup-uv v10.3.3, plus setup-node v7.1.0 for TypeScript. (#229)

## [1.1.0-rc.4] — 2026-11-07

Fourth release candidate of 1.1.0, themed "Evidence". Relational world-effect contracts, semantic refresh diffs, per-operator mutation scores and executable public claims make the evidence behind a green gate visible. Changes that alter results are marked **Behavior change** below.

**Upgrade note:** Re-pin baselines containing effect assertions when upgrading from 1.1.0-rc.3 or earlier. Baseline comparison exits 3 when either a baseline or report contains legacy effect identities without `effect_world_excluded: true`; re-pin the baseline and re-run reports before comparing. The assertion id scheme remains 2.

### Added

- `attest capture --refresh` prints a per-exchange semantic diff (system prompt lines, tool schemas, messages, tool results, model, params) and reports "no semantic change" when nothing differs. `--json` results carry a `diff` object.

- Effect checks `sum` and `matches` relate two world resources: a refunded total stays within the order amount, a notification goes to the order's customer. The contract DSL gains `sum(...)`, `matches(...)` and the `related(resource, field, on=key)` literal, and `draft_contract` drafts them from a world.

- TypeScript SDK: `sum()` and `matches()` world-effect checks and a `related()` helper for relational effects.

- Python SDK: `sum` and `matches` world-effect checks and the `related()` helper for relational effects (refunded total within the order amount; notification goes to the order's customer).

- `attest mutate --json` reports `operators` (per-operator generated, killed, survived, no-applicable and score) and `excluded` (skipped specs by reason, mutants without an applicable spec); the text table prints the same rows, and `attest view` renders them.

- `alter_world_effect` raises a numeric field above its related record's value and redirects a shared `*_id` field, so `sum` and `matches` effect specs can kill world mutants and survivors suggest them.

- `attest-engine capabilities [--json]` prints the layers, assertion types, SDK languages, adapters and features with maturity tiers, the licence and versions; schema `proto/attest/v1/cli/capabilities.schema.json`.

- `make generate-claims` and `make check-claims` render and verify the README layer table, adapter list and feature maturity table and the adapter guide's tier table from `capabilities --json`.

- Issue templates "Report a regression attest missed" and "Design partner".

- `related()` is exported from both SDK roots (`attest`, `@attest-ai/core`).

- Guides for relational `sum`/`matches` effects, relational drafting through MCP, the `--refresh` semantic diff and the per-operator mutation report.

- CI `claims` job runs `make check-claims`, and CI `snippets` job runs every runnable README and guide code block offline; both are required by the `required` gate.

- `scripts/run_doc_snippets.py`: snippet runner with `<!-- snippet: ... -->` markers; every skip and fragment carries a reason and is counted.

- `attest capture` drafts relational `sum`/`matches` effect contracts from the captured world and verifies them against it; with `--expect-fail` they are recorded as expected failures. A capped draft list is reported as `relational_capped` in the `--json` result and in the summary. `fixture-v1` gains the `relational` kind and `effect` type.

### Changed

- `count` with a fractional or `related` `at_most` now fails with `invalid effect spec: count: at_most must be an integer` instead of a JSON decode error.

- The `attest mutate` text table columns are now `operator generated killed survived n/a score` (was `operator mutants killed survived`).

- **Behavior change:** a world number whose decimal exponent is outside ±400 is now an evidence error (`effect evidence: … decimal exponent outside ±400`) instead of being evaluated; relational sums print exact values up to 1024 digits and a numerator/denominator form beyond that.

- Public docs describe six evaluated layers plus plugins; the internal plugin layer id 8 is unchanged. Multi-agent assertions are documented as layer 3.

- The README lists the 10 Python adapter modules that exist (it listed 11 including a non-existent Simulation adapter) and a maturity tier per adapter and feature.

**Behavior change:** a wrong method name, a stale import or an unmarked non-runnable code block in the README or a guide now fails CI; before, it passed silently.

**Behavior change:** a world capture without `--expect-fail` whose world violates a drafted relational contract is now `not_verified` (exit 1); before, relational contracts were not drafted and the capture passed.

### Fixed

- **Behavior change:** world numbers with a decimal exponent outside ±400 fail as effect evidence errors instead of passing.

- `@attest-ai/core` and `@attest-ai/vitest` declare `Apache-2.0` (they declared MIT) and ship the `LICENSE` file.

- Removed the README claims of a Go SDK, of SDK/engine release decoupling (the SDK requires the exact engine version) and of "70% of your agent's testable surface is deterministic" (no source).

- `docs/guides/capture.md` fixture table lists `fixture_version` and `name`; the `protocol-spec.md` handshake example shows the current version.

- **Behavior change:** effect assertion ids and spec hashes exclude their runtime world snapshot, so a baselined passing effect contract that fails on changed world state is blocked by `block_on_regression` rather than silently classified as a spec edit. Re-pin baselines containing effect assertions when upgrading from 1.1.0-rc.3 or earlier; id scheme stays 2.

- **Behavior change:** baseline comparison exits 3 with a re-pin/re-run hint when either side contains legacy effect identities without `effect_world_excluded: true`, preventing upgrade-time fail-open policy results. Non-effect-only legacy comparisons remain compatible.

- Require assertion-type context in low-level canonical-spec helpers and preserve non-object effect specs during identity encoding.

## [1.1.0-rc.3] — 2026-11-06

Third release candidate of 1.1.0, themed "Trusted supply chain". Every GitHub Actions reference in the repository and in the `attest init --ci github` scaffold is pinned to a commit SHA and checked in CI, the scaffolded workflow gives pull-request write access only to a separate `annotate` job, and `pr-annotate` reports every report's policy result. `@attest-ai/vitest` also loads on Vitest 5. Changes that alter results are marked **Behavior change** below.

### Changed

- Every GitHub Actions `uses:` in this repository's workflows and composite actions is pinned to a commit SHA with a version comment; Dependabot's `github-actions` ecosystem now covers the workflows and both composite actions, grouped. `scripts/check-action-pins.py` runs in the `GitHub config` CI job.
- `attest init --ci github` (Python and TypeScript) writes a workflow with SHA-pinned actions and two jobs: the test job holds `contents: read`, and a separate `annotate` job holds `pull-requests: write`. The annotate step pins the `pr-annotate` `v1.0.1` commit instead of `@v<sdk version>`.
- **Behavior change:** a workflow or action that references an action by tag now fails the `GitHub config` check.

### Fixed

- `pr-annotate`: with several reports the policy line now shows the worst exit, every report's violations (each prefixed with its report) and a per-report list instead of only the worst report.
- CI: removed the `notify-docs-python` and `notify-docs-typescript` jobs, which passed an empty `DOCS_DEPLOY_TOKEN` on push to main; the release workflow dispatches the website sync from the `website-sync` environment.
- `@attest-ai/vitest` now supports Vitest 5 (peer `>=3.0.0 <6`); it previously failed to load there.

## [1.1.0-rc.2] — 2026-11-06

Second release candidate of 1.1.0, themed "Real adapters, real parity". The runner integrations behave the same on every supported version: `@attest-ai/vitest` loads and reports cost on Vitest 3 and 4, and the Python helpers keep trace metadata, accept plain agents and fail an offline run in one line. Safe defaults landed early: shipped skills no longer auto-confirm a recorded command, `--attest-egress=block` covers provider calls through HTTP proxies, and `attest capture` templates session identifiers and redacts every server-sent event. Changes that alter results are marked **Behavior change** below.

### Changed

- Guides document the `@attest-ai/vitest/reporter` and `/setup` subpaths for Vitest 3 and 4, the removal of `AttestCostReporter`, the Python `set_metadata` merge, string-returning `@agent`, and the one-line offline engine failure.
- CI runs the `@attest-ai/vitest` suite on the newest Vitest 3.x and 4.x and runs `pnpm lint` for the TypeScript SDK; both are required checks.
- **Behavior change:** a TypeScript lint finding or a failure on either Vitest major now fails the `required` check.

### Fixed

- `AttestReporter` and `@attest-ai/vitest/setup` load on Vitest 3 (they imported `vitest` at runtime, failing with "Vitest failed to access its internal state"); the peer range `>=3.0.0 <5` is now exercised on both majors.
- `@attest-ai/vitest/setup` exports `setup` and `teardown`, so `globalSetup: ["@attest-ai/vitest/setup"]` works and downloads the engine once before tests run.
- The unevaluated-`expect()`-chain guard now fails the right test on Vitest before 4.1.
- `pnpm lint` in `sdks/typescript` runs Biome with a committed config; it checked nothing before.
- `TraceBuilder.set_metadata` merges fields across calls instead of replacing the metadata.
- `@agent` wraps a string return as `{"message": ...}`, accepts positional arguments and honors `adapter=` for provider adapters.
- A failed engine download ends the run with one line naming `ATTEST_ENGINE_PATH`; the download notice prints outside pytest capture.
- **Behavior change:** a cost or latency breach set through separate `set_metadata` calls now fails instead of skipping as `missing_evidence`.
- **Behavior change:** `@agent(adapter=...)` with a non-provider, non-manual adapter raises `TypeError` at decoration instead of being ignored.
- A TypeScript engine that exits during startup now fails with its stderr: `EngineManager.launch` waits (bounded, 1 s) for the exit report on stdout EOF before terminating the child.

### Security

- Shipped skills no longer tell agents to pass `--yes` to `attest capture --refresh`. They show the recorded `source.command` with its SHA-256 and stop for human confirmation.
- **Behavior change:** `--attest-egress=block` / `ATTEST_EGRESS=block` now also refuses provider calls made through `HTTP(S)_PROXY` (CONNECT and absolute-URI targets), in both SDKs; calls that previously passed through a proxy now fail with `EgressBlockedError`.
- The egress guard's provider host list now includes AWS Bedrock, Vertex AI, Mistral, Groq, OpenRouter and Together.
- Documented limit: child processes, SOCKS proxies and TLS-wrapped proxy connections are not covered.
- **Behavior change:** `attest capture` and `attest capture --check-redaction` refuse a `.env` that is not a regular file or is over 1 MiB; `attest replay` (and `--serve`) warns once on stderr and ignores it.
- **Behavior change:** a replay recorded before this change computes a different key for requests that carry `metadata.user_id` or Claude Code environment reminders; keys are recomputed on load, so existing fixtures still replay, and `attest capture --refresh` rewrites them with templated identifiers.
- `attest capture` templates Claude Code `metadata.user_id` (device, account and session ids) and leaves it out of the replay key, and normalizes environment `<system-reminder>` blocks for matching, so a replay in a new session hits.
- `attest capture` decodes and redacts every server-sent event, so a secret containing a quote or backslash in a non-delta event such as `response.completed` no longer reaches the fixture; `attest capture --check-redaction` flags it without printing it.
- Redaction covers JWTs, Azure, Mistral and Cohere keys introduced by `api-key`, `subscription-key`, `access-key` or `client_secret`, those names as query parameters and headers, and the secret values in the working directory's `.env`.
- Replay-key normalization of Claude Code environment values is limited to Claude Code's own sections (`# Environment`, `<env>`, `# currentDate`, `gitStatus:`) and covers the indented entries under `Additional working directories:`; `Status:` and similar lines in an application's own prompt keep their meaning.
- Redaction matches secrets escaped twice (JSON arguments inside raw JSON text), and a `.env` value with an unterminated quote no longer hides the variables after it.

### Removed

- **Behavior change:** `AttestCostReporter` is no longer exported from `@attest-ai/vitest`. It printed nothing on Vitest 4. Use `AttestReporter` (`reporters: ["default", "@attest-ai/vitest/reporter"]`): it prints the `attest usage:` line from per-test meta and writes the cost to the report-v2 file.

## [1.1.0-rc.1] — 2026-11-06

First release candidate of 1.1.0, themed "Fail closed". Judges are treated as measuring instruments: a malformed reply or a rejected credential is `not_evaluated`, never a pass, and only transient failures are retried. Green now means checked: assertion ids are stable (scheme 2), a removed assertion blocks the policy, and a test with an unevaluated `expect()` chain fails. The engine exits on signals and refuses invalid configuration, dynamic thresholds and continuous-eval drift alerts work, adapters conform to a shared behavioral corpus, and the SDK packages carry signed engine hashes. Several changes turn a silent pass into a failure; they are marked **Behavior change** below.

### Upgrading from 1.0.x

- Re-pin baselines: assertion ids are scheme 2 (`a2_` + 32 hex), and `attest policy evaluate --baseline` exits 3 for a baseline pinned on id scheme 1.
- Set `ATTEST_ENGINE_TIMEOUT` in seconds for the TypeScript SDK (it was milliseconds; values above 3600 are refused).
- History ids change, so drift baselines rebuild over the first runs after upgrading.
- Source and editable installs no longer download an engine: build one with `make engine` and point `ATTEST_ENGINE_PATH` at it.
- `ContinuousEvalRunner` requires a name (`name=` in Python, `options.name` in TypeScript), and the TypeScript `stop()` returns a `Promise<void>`.
- Tests that never evaluate an `expect()` chain now fail; evaluate the chain or remove it.
- Tests that asserted the old TypeScript OpenAI adapter shapes, the CrewAI `agent_call` / `tool_call` steps or an empty output on an OpenAI refusal must be updated.

### Added

- `failure_class: spec_error` for invalid judge specs, and `initialize` capabilities `judge_configured` and `embedding_configured`.
- `judge_malformed` and `provider_auth` not_evaluated reasons in the report-v2 and verdict schemas, protocol fixtures and both SDKs' constants (`REASON_JUDGE_MALFORMED`, `REASON_PROVIDER_AUTH`, `FAILURE_CLASS_SPEC_ERROR` / `FailureClass.SpecError`); `attest explain_failure` (MCP) names the next step for both reasons.
- Report v2 fields `id_scheme`, `budget_exceeded`, and per-row `scope_id` and `spec_hash`; policy rules `block_on_removal` and `allow_removed`; a plugins guide.
- `attest-engine replay --trace-out <file>` writes the trace of the new run; `replayRun` (`@attest-ai/core`) and `attest.capture.replay_run` re-run a fixture's source command under strict replay and return the new trace.
- Continuous-eval guide covering runner setup in Python and TypeScript, the webhook and Slack alert payload, triggers, error handling, stop and drain, and the 10-run cold start. Documentation for `threshold="dynamic"` on `passes_judge` and `output_similar_to`.
- `AttestClient.queryDrift` (TypeScript) and `AttestClient.query_drift` (Python) accept a static threshold.
- Python `ContinuousEvalRunner` takes an `on_error` callback.

### Changed

- **Behavior change:** CI now fails when a framework-gated adapter test is skipped because its framework or the engine is missing; previously these skipped silently.
- **Behavior change:** CrewAIAdapter traces are summary-only (output, tokens, latency). Agents and tasks from the crew's configuration are no longer recorded as `agent_call` and `tool_call` steps, so assertions on those steps now fail.
- **Behavior change:** `ContinuousEvalRunner.stop()` returns a `Promise<void>` and waits (bounded) for in-flight evaluations and alert dispatches.
- **Behavior change:** `ContinuousEvalRunner` requires a `name=` keyword, the scope of its assertion ids.
- **Behavior change:** `ContinuousEvalRunner` requires `options.name`.
- Guides document the judge reply contract, per-call nonce delimiters, selective retry (408/429/5xx/network; 401/403 is one call), the ACP environment allowlist, `ATTEST_ENGINE_TIMEOUT` in seconds in both SDKs, the new initialize capabilities and the 1.1.0 JSON-RPC and numeric-environment behavior.
- CI lints the engine with golangci-lint and tests the Python SDK on 3.13 and 3.14, and runs the adapter tests against real crewai, langchain-core, google-adk, llama-index-core and provider SDKs from `uv.lock`. The `crewai` extra is not installed on Python 3.14 (its dependency chain pins a pydantic-core release without 3.14 wheels).
- `attest capture` output, drafted tests and the capture guide say "consistent with the recording" instead of "verified", and state that replay keeps relative, not absolute, identity.
- Corrected `results[].drift.count` in the protocol spec to the number of baseline rows.

### Fixed

- **Behavior change:** a judge reply with a missing, non-numeric or out-of-range `score` is now `not_evaluated(judge_malformed)`; it was a measured pass (`{"score":7}`) or a measured 0 hard fail.
- **Behavior change:** provider 401/403 responses are no longer retried; the assertion is `not_evaluated(provider_auth)` after one judge call (previously four calls, 3.5 s, `provider_unavailable`). Other non-transient errors (400, 404, unparseable replies) are also no longer retried.
- LLM provider retries use jittered backoff and honour `Retry-After` on 408/429/5xx.
- Judge prompts delimit agent output with a per-call random nonce; output containing the old fixed delimiter cannot close the data block, and a judge reply containing the nonce is `judge_malformed`.
- The judge cache is consulted only after the spec validates, and its key includes `repeat` and bias probes.
- `failure_class` for judge failures is computed against the assertion's own threshold, so a 0.78 score against 0.8 is `stochastic_variance` rather than `broken_code`.
- The ACP judge agent starts with an allowlisted environment (process basics, locale, Windows system variables, proxy/CA settings, `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME`) instead of inheriting everything but ten variables, so unrelated credentials such as `GITHUB_TOKEN` and `AWS_SECRET_ACCESS_KEY` no longer reach it.
- **Behavior change:** ACP agents that relied on other inherited variables (for example `XDG_*`, `NPM_CONFIG_*`) no longer see them.
- **Behavior change:** a test that builds an `expect()` chain and never evaluates it now fails with "N expect() chain(s) built but never evaluated"; evaluating a chain with no assertions fails. Both used to pass.
- **Behavior change:** a Vitest test that builds an `expect()` chain and never passes it to `evaluate()`/`inspect()`, or evaluates a chain with no assertions, now fails instead of passing.
- **Behavior change:** assertion ids are now `a2_` plus 32 hex characters over the test node id (or `ContinuousEvalRunner` name), assertion type, canonical spec and occurrence index. Reordering assertions keeps their ids, identical assertions get distinct ids, and editing a spec makes a new id. Baselines pinned on 1.0.x must be re-pinned.
- Both SDKs derive assertion ids this way.
- **Behavior change:** `attest policy evaluate --baseline <tag>` exits 1 when a baselined assertion id is missing from the report (`block_on_removal`, on by default with a baseline). List accepted removals under `allow_removed`; a spec edit is reported as `changed` and does not block.
- **Behavior change:** `attest policy evaluate --baseline` exits 3 with `baseline uses id scheme 1; re-pin it on 1.1` for baselines pinned before id scheme 2.
- **Behavior change:** a run that exceeded `ATTEST_MAX_SOFT_FAILURES` blocks the policy (`budget_exceeded`); `evaluate_batch` now returns every result with a `budget_exceeded` flag instead of `ENGINE_ERROR`, so hard failures are no longer dropped. Reports carry the flag, true when any batch pushed the session past the limit.
- `.plugin()` assertions run their registered plugin and submit the result before evaluation, so the row follows the plugin result and judge and embedding assertions in the same batch still evaluate (Python: register plugins on `attest_engine.plugin_registry` or through the `attest.plugins` entry point; TypeScript: `attestPlugins`).
- **Behavior change:** the engine exits 0 within about a second of SIGTERM or SIGINT, cancelling an in-flight judge call, instead of finishing the batch and exiting 1.
- **Behavior change:** the engine and `attest-engine doctor` refuse `ATTEST_EVAL_CONCURRENCY`, `ATTEST_EMBEDDING_CACHE_MAX_MB`, `ATTEST_JUDGE_CACHE_MAX_MB`, `ATTEST_HISTORY_MAX_ROWS`, `ATTEST_HISTORY_MAX_AGE_DAYS` and `ATTEST_JUDGE_TIMEOUT_S` values that are not positive integers (doctor exits 1 naming each variable). `ATTEST_JUDGE_TIMEOUT_S` no longer falls back to 30 on a bad value.
- **Behavior change:** JSON-RPC requests without an `id` are no longer answered; parse errors and invalid ids reply with `id: null` instead of `0`. String ids are echoed unchanged.
- `capture`, `replay` and `world` no longer die and orphan the wrapped command when SIGINT or SIGTERM arrives just as the command starts; the signal is relayed to the command.
- The engine Go module path is `github.com/attest-framework/attest/engine`, so `go install github.com/attest-framework/attest/engine/cmd/attest-engine@<version>` resolves; the CONTRIBUTING clone URL points at `attest-framework/attest`.
- **Behavior change:** the TypeScript SDK reads `ATTEST_ENGINE_TIMEOUT` in seconds (was milliseconds) and refuses values above 3600 with "ATTEST_ENGINE_TIMEOUT is in seconds since 1.1.0".
- **Behavior change:** the repository's TypeScript test run fails once with a `make engine` remedy when `bin/attest-engine` is missing or stale, instead of skipping or failing each engine-backed test. `uv run pytest` in `sdks/python` works on a fresh clone via a `dev` dependency group.
- **Behavior change:** `threshold="dynamic"` on `similar_to` and `passes_judge` is evaluated against history instead of always hard-failing as an invalid spec.
- **Behavior change:** the dynamic pass bar is never below the static threshold, uses a sigma floor of 0.02, and is built from passing and soft scores only. An all-zero or failing history no longer passes a 0.0 score, and a steady 1.0 history no longer hard-fails 0.99.
- **Behavior change:** `query_drift` and `result.drift` use the same estimator as classification; `query_drift` ignores `window_size`, and `result.drift` is attached only when a 10-run baseline exists.
- **Behavior change:** history is keyed by id scheme 2 assertion ids. Rows recorded under ordinal ids are orphaned, so the first runs after upgrading rebuild each baseline.
- `ContinuousEvalRunner` (Python and TypeScript) delivers drift alerts: after each evaluation it reads drift and posts to the alert webhook / Slack URL on `drift_detected` or `hard_fail`. Dispatch errors go to `on_error` (Python) or `alertError` / `onError` (TypeScript) and the log, Python POSTs are time-bounded, and `stop()` waits for in-flight evaluations and dispatches.
- Python `ContinuousEvalRunner` reads the alert webhook and Slack URL configured through `attest.config()` (or the `ATTEST_ALERT_*` env vars) when none are passed; TypeScript `stop()` processes the traces already queued before resolving, bounded by `drainTimeoutMs`.
- **Behavior change:** strict replay keeps the roles of email identities across a session. A request that notifies a different address than the one looked up no longer replays a recording where both were the same address. A run that uses one different address throughout still replays.
- **Behavior change:** the test `attest capture` drafts for a command capture re-runs the agent under strict replay and fails when the new run misses the recording or violates a contract; it used to pass whatever the agent did.
- **Behavior change:** an OpenAI refusal now fails `output_not_contains` on the refusal text; before, the output was empty and the assertion passed.
- **Behavior change:** TypeScript adapter traces now match the Python adapters: `OpenAIAdapter` tool-call args are objects (was the raw JSON string, which failed `toolArgsMatchSchema`), refusals are the output text (so `outputNotContains` on refusal wording now fails), and llm steps carry token counts. Tests that asserted the old shapes must be updated.
- **Behavior change:** `LangChainAdapter` methods now take langchainjs's callback signatures (`..., runId, parentRunId`). The old hand-called forms (`handleChainStart()`, `handleLLMEnd(output)`, `handleToolEnd(output, {name})`) are gone: langchainjs never called them, because `fromMethods` copied only own properties. `new LangChainAdapter()` now throws unless the `@langchain/core` peer is installed.
- The `LangChainAdapter` change applies to the TypeScript SDK. It is a `BaseCallbackHandler` keyed by `runId`; pass it in `callbacks`.
- Adapters: OpenAIAdapter handles Responses API objects (Python and TypeScript) and records a model refusal as the completion and trace output; AnthropicAdapter records `server_tool_use` blocks as tool calls; GeminiAdapter skips thought-summary parts, reads the model from `model_version` / `modelVersion`, reads usage tokens and no longer raises on a function-call-only response (TypeScript `input` is `{text}` for a single string message); OllamaAdapter (TypeScript) no longer records an empty `model` arg.
- Provider adapters set `latency_ms` from the timestamps passed in (`startedAtMs` / `endedAtMs` in TypeScript) and record input, output and total tokens on the llm_call step, with tool calls after it. LangChainCallbackHandler (Python) records tool arguments as structured objects, reads ToolMessage content blocks and `usage_metadata` tokens, and measures latency from event timestamps.

### Security

- **Behavior change:** the SDK engine download is verified against a hash table embedded in the wheel and npm tarballs instead of the release's `checksums-sha256.txt`. A binary that differs fails with `does not match the hash shipped in attest-ai <version>` / `@attest-ai/core <version>`.
- **Behavior change:** a source or editable install no longer downloads an engine; it fails naming `ATTEST_ENGINE_PATH` and `make engine`.
- Engine downloads refuse a redirect to a non-https URL, resolve relative redirects, and time out (10s connect, 120s total). A truncated download is reported as truncated rather than as a hash mismatch, and a malformed redirect `Location` is a normal download failure in both SDKs instead of an uncaught exception.
- Release engine binaries carry GitHub build-provenance attestations, created by a dedicated `attest-engine` job that alone holds `id-token: write` and `attestations: write`; verify with `gh attestation verify <file> --repo attest-framework/attest --signer-workflow attest-framework/attest/.github/workflows/release.yml --source-ref refs/tags/v<VERSION>`.
- The Python engine download honours `HTTPS_PROXY`/`NO_PROXY` and system proxies (CONNECT tunnel, end-to-end TLS verification); proxy credentials never appear in errors.

### Removed

- `@attest-ai/vitest` no longer ships `engine-checksums.json` (it has no downloader).
- Empty `engine/pkg/engine`, `engine/internal/config` and `sdks/go` directories, unused helpers, and the `engine-benchmark` and `protocol-benchmark` Makefile targets.

## [1.0.1] — 2026-11-05

Hotfix themed "Red stays red". On 1.0.0 a failing assertion could be reported as green: an engine drift line, a request timeout or concurrent evaluations could hand one test another test's verdict, a pytest-xdist run wrote an empty report that the policy gate passed, and a regression policy without its baseline passed silently. 1.0.1 routes every engine response by request id in both SDKs, writes complete reports under parallel runners, and makes `attest policy evaluate` fail closed when an input is missing. Several fixes turn a silent pass into a failure; they are marked **Behavior change** below.

### Fixed

- **Behavior change:** Python and TypeScript SDKs route every engine response by request id. A test that previously passed on another request's response (after an engine `drift_alert` line, after an `ATTEST_ENGINE_TIMEOUT`, or under `it.concurrent`) now reports its own, possibly failing, verdict. Lines without an id are dropped and logged.
- **Behavior change:** `evaluate_batch` raises `ProtocolDesyncError` (Python) or rejects with it (TypeScript) when the response's assertion ids differ from the ids sent.
- **Behavior change:** a request timeout restarts the engine; the timed-out request raises (rejects with) `EngineTimeoutError` and every other pending request fails, so a late reply never reaches another test. The new engine gets the remaining `ATTEST_MAX_JUDGE_CALLS` / `ATTEST_BUDGET_USD` session budget: the configured limit minus the usage already returned, minus a hold for each lost request that can call a model (its per-batch `budget` if it sent one, otherwise everything left). Spend the SDK cannot observe (simulated-user messages, engine error replies, replies dropped after cancellation) leaves the restarted engine no budget, so a restart can never refill the session budget.
- **Behavior change:** `pytest -n N --attest-report=<path>` writes one report containing every worker's tests and assertions; before, the report was empty and `attest policy evaluate` passed. If a worker ends without sending its report, the run exits with an internal-error status and no report is written (an older file at the path is removed).
- **Behavior change:** `attest policy evaluate` exits 3 instead of passing when the policy sets `block_on_regression` or `regression` without `--baseline`, or `min_mutation_score` without `--mutation`; the message names the missing input.
- **Behavior change:** `attest policy evaluate` exits 3 when a report has no tests while its `tests_collected` is greater than 0 (a parallel run that lost its results).
- The engine reports drift inside `evaluate_batch` results (`results[].drift`) instead of writing an id-less `drift_alert` line to stdout.
- Python: responses over 64 KiB (for example 400+ assertions) no longer crash the session; the SDK's stream limit matches the engine's 10 MiB request line limit.
- TypeScript: `EngineManager.start()` rejects when the engine exits or closes before it is ready, with the engine's stderr tail (also when the engine dies before the first write, which used to surface as a bare `write EPIPE`), and `initialize` times out instead of hanging. Write errors after startup carry the stderr tail.
- TypeScript: request timers are cleared, so a standalone script exits when its work is done instead of 30 s later.
- Python: `EngineManager.send_request` stays public and id-correct (it goes through an attached `AttestClient`, otherwise the id-matched handshake), and `EngineManager.stop()` shuts down cleanly when an `AttestClient` reader is attached. A second `AttestClient` on one engine raises `RuntimeError` instead of stealing the first one's responses.
- Engine history and caches share one SQLite handle with WAL and `busy_timeout`, so parallel engine processes no longer lose history rows to `SQLITE_BUSY`; `attest baseline pin` and `attest calibrate` open the database the same way and wait instead of failing.
- History pruning runs in the background in short batched deletes (age through a new `created_at` index, the row cap per recently recorded assertion id) instead of a whole-table delete inside `evaluate_batch`, removing the stall near 2,800 evaluations per assertion.
- The `attest-ai` PyPI quick start is a runnable pytest test that evaluates its chains and fails on a wrong assertion; it used an undefined `trace` and built chains it never evaluated.
- Guide examples in framework-adapters, multi-agent and simulation evaluate their chains and use existing methods (`required_tools()` replaces the nonexistent `tool_called()`).

### Added

- report-v2 files written by the pytest plugin and the Vitest reporter carry a top-level `tests_collected` (tests collected after deselection); `report-v2.schema.json` declares it.

### Changed

- The protocol spec states that the engine writes only responses to stdout and that SDKs route by request id; `docs/guides/configuration.md` documents `ATTEST_ENGINE_TIMEOUT` (seconds in Python, milliseconds in TypeScript) and what a timeout does to the session budget, and notes that budgets apply per pytest-xdist worker.
- Install instructions use `attest-ai>=1.0.1` and `@attest-ai/core@>=1.0.1` / `@attest-ai/vitest@>=1.0.1`; `@attest-ai/vitest` declares the peer `@attest-ai/core >=1.0.1`. The annotations guide pins `pr-annotate@v1.0.1`.
- The PyPI classifier is `Development Status :: 4 - Beta` (was `5 - Production/Stable`) until 1.1.0 completes its re-evaluation.
- Engine Go dependencies: `go.opentelemetry.io/proto/otlp` 1.11.1, `golang.org/x/text` 0.43.0, `modernc.org/sqlite` 1.61.1, `google.golang.org/protobuf` 1.36.12.

### Removed

- The `drift_alert` stdout notification (`types.DriftAlertNotification`).

## [1.0.0] — 2026-11-04

First stable release. A real run or incident becomes a verified fixture with `attest capture`; pull-request CI replays the agent's real code against it with zero model calls and returns pass, fail or not evaluated (with a reason) to a policy gate, in Python and TypeScript. The release candidates added each piece: honest verdicts and install integrity (rc.1), trace checks, behavior contracts and judge budgets (rc.2), capture, replay, world fixtures and pull-request annotations (rc.3), agent-facing CLI, MCP server and mutation testing (rc.4), TypeScript parity, framework capture through OpenTelemetry and the offline failure viewer (rc.5). The [refund-desk](https://github.com/attest-framework/refund-desk) demo walks the whole workflow with `make walkthrough`. Changes since rc.5:

### Changed

- Install instructions in the README, the Python SDK README (the PyPI page), the guides and the `attest` skill's migration reference use `attest-ai>=1.0.0` and `@attest-ai/core@>=1.0.0` / `@attest-ai/vitest@>=1.0.0` (with `vitest@^4`) instead of the release-candidate floors; with 1.0.0 published, a bare install resolves to it. The annotations guide's example pins `pr-annotate@v1.0.0`. `@attest-ai/vitest` declares the peer `@attest-ai/core >=1.0.0` (was `>=1.0.0-rc`), so it no longer installs next to a release-candidate core with a different engine version. The PyPI classifier is `Development Status :: 5 - Production/Stable`.

### Fixed

- `attest capture` never reports a false green. A capture exits 1 (`not verified`, naming every cause that holds) when a drafted contract failed, the trace check failed, `--expect-fail` drafted nothing, or nothing was drafted while the command exited non-zero or a replay-capable capture recorded no model call. Before, a command that exited non-zero with no reachable model printed `verified: every drafted contract passes` with zero drafted contracts and exited 0. A non-zero exit whose drafted contracts all pass still verifies (exit 0, the code recorded). `attest trace check` warns on a not-verified fixture with the same causes.
- A healthy run with nothing to draft a contract from (no tool call, structured output or cost) has the new outcome `nothing_to_verify`: the fixture is written with `verified: false`, `attest capture` exits 0 and prints `captured: nothing to verify (no tool call, structured output or cost); replay-only fixture, no test drafted`, and no test is drafted. `attest capture --json` and `--result-file` gain `outcome` (`verified` | `not_verified` | `nothing_to_verify`; `proto/attest/v1/cli/capture.schema.json`, with valid and invalid fixtures). Python `record()` and TypeScript `record()` return `verified=False` with `outcome="nothing_to_verify"` instead of raising, and still raise `CaptureError` for `not_verified`. The committed judge fixtures were re-recorded and no longer claim `verified: true` with no contracts.
- Re-capturing the same scripted run writes the same `trace.conversation_id`: clock-derived conversation ids (`trace.conversation_id`, the `gen_ai.conversation.id` span attribute) are templated to `{{conversation_N}}`. The capture guide now names what still differs between captures instead of promising identical fixtures: raw span ids in `turns[].from_agent` (OpenAI Agents), LangChain message and `lc_run--` ids in LangSmith strings, and the Claude Code session id in `metadata.user_id` of the recorded request (Claude Agent SDK), which is part of the replay key's params.

## [1.0.0-rc.5] — 2026-11-04

Fifth release candidate, themed "Parity and reach": the TypeScript SDK gains the simulation helpers and plugin discovery Python has, with a generated parity matrix that fails the test suites on an undocumented gap; `attest capture` records runs from Pydantic AI, the Vercel AI SDK, Mastra, the OpenAI Agents SDK (Python), the Claude Agent SDK and LangGraph (Python) through their OpenTelemetry instrumentation; and `attest view` renders a self-contained offline failure page with the failing span highlighted and a baseline diff.

### Added

- `attest view <report.json...> [--baseline <report.json>...] [--mutation <mutate.json>] [--out FILE] [--no-open] [--json]` writes one self-contained offline HTML page: the test list, the trace tree with the failing span highlighted, expected versus actual, replay or live provenance, cost, a baseline-versus-head diff (pass rate, cost, latency, changed tool sequences) and surviving mutants. The page has no JavaScript and no external resources, carries a `default-src 'none'` Content-Security-Policy, escapes all report content and bounds every field and the page size. It opens in a browser only on a terminal outside CI. `--json` schema `cli/view.schema.json`; exit 0 written, 2 usage or input error. Guide: `docs/guides/viewer.md`.
- `ATTEST_REPORT_TRACES=1` makes the pytest plugin and the Vitest reporter add a top-level `traces` map (trace id to trace-v2) and a `trace_id` on each result row to report-v2. Both fields are optional in `report-v2.schema.json`; `report_version` is unchanged. Traces hold prompts, outputs and tool arguments, so the variable is off by default.
- `attest init --ci github` (Python and TypeScript) adds a `Write the failure viewer` step and uploads `attest-report.html` in the `attest-report` artifact. The test step carries `ATTEST_REPORT_TRACES: "1"` commented out with a warning: traces are not redacted and artifacts of public repositories are downloadable by any signed-in GitHub user, so enable it only on private repositories.
- Framework capture through OpenTelemetry, with no attest-specific adapter: `docs/guides/capture.md` gains a Frameworks section with the instrumentation switch, tested versions and caveats for Pydantic AI, the Vercel AI SDK, Mastra, the OpenAI Agents SDK (Python), the Claude Agent SDK (Python and TypeScript) and LangGraph (Python). Golden OTLP fixtures for each route join `protocol-tests/trace-v2/otlp/`.
- The OTLP parser keeps `tool.output` span events (the first four per span, among the first 256 events); all other events are never read. A malformed `tool.output` event is dropped and `trace check` warns `malformed_event`. Claude Code records a tool's result there.
- Claude Code native traces (beta): `claude_code.llm_request` is a model call with its native model, token and cache-token attributes; `claude_code.tool` takes its result from its `tool.output` event; `claude_code.tool.execution` and `claude_code.tool.blocked_on_user` are not steps. A native attribute of the wrong type is skipped with a `malformed_native_attribute` warning from `trace check` instead of failing conversion.
- Tool spans without `gen_ai.tool.call.arguments` or `gen_ai.tool.call.result` fall back to `gen_ai.prompt` and `gen_ai.completion`, decoding LangSmith's result wrapper shapes on LangSmith spans.
- TypeScript simulation helpers in `@attest-ai/core`: `scenario`, `faultInject`, `mockTool`, `MockToolRegistry` and `repeat`, with `ScenarioResult` and `RepeatResult` aggregating verdicts like Python's. `TraceBuilder.addToolCall` returns the active mock's result. Guide: [simulation](docs/guides/simulation.md).
- TypeScript `useAttest().enforce(verdict)` and `enforceOutcome` accept a `ScenarioResult` or `RepeatResult` as well as an `AgentResult` (fail with diagnostics, skip with the not-evaluated reasons, strict mode fails skips), as Python's `attest.enforce` does.
- TypeScript plugin discovery: `loadPackagePlugins(registry, projectRoot?)` loads plugins that an installed dependency declares in `package.json` under `"attest": {"plugins": [...]}`; `registerPlugin` mirrors Python's `register_plugin`.
- A parity matrix, [docs/guides/parity.md](docs/guides/parity.md), generated from both SDKs' public surfaces (`sdks/parity/`). The existing Python and TypeScript test commands fail on a Python capability with no TypeScript counterpart or documented exception, on a stale exception, and when the snapshots or the guide are out of date (regenerate with `ATTEST_UPDATE_PARITY=1`).

### Changed

- A `tool_cardinality` check that fails on `max` now sets `trace_node_path` to `steps[N]`, the first call over the limit, instead of `trace.tool_cardinality`; a `min` violation keeps the old path. Report-v2 rows and the SDK diagnostics output (`trace path:`) show the new value; `attest report rows` output does not change.
- A span whose `gen_ai.operation.name` is present but unknown (`chain`, `agent_step`, `unknown`, ...) is no longer a step; its children still attach to the nearest enclosing step. Spans with no operation attribute keep the attribute heuristics.
- `attest capture` does not verify a fixture where no `llm_call` or `tool_call` step was mapped (exit 1, with the cause and a pointer to the capture guide). The `trace check` `no_genai_spans` message points to the same guide section.

### Fixed

- Finish reasons `tool-calls` (Vercel AI SDK, Mastra) and a JSON array in a string, as Mastra records it, count as tool requests in `trace check`; it no longer warns `unrequested_tool` on those exports.
- TypeScript `executePluginAssertion` clears its timeout timer when the plugin finishes, so a completed assertion no longer holds the process open for the timeout.

## [1.0.0-rc.4] — 2026-11-04

Fourth release candidate, themed "Agent-native": coding agents operate attest without the website through `--json` output with published schemas and stable exit codes, offline guides (`attest guide`), installable skills and plugins, and an MCP server whose closed contract grammar drafts and checks contracts. `attest mutate` measures how much of an agent's behavior the suite would actually catch.

### Added

- `attest mutate <fixture.attest.json...> [--contracts DIR] [--seed N] [--max-mutants N] [--json]` tests the tests: it builds behavior mutants of each fixture (drop, duplicate or reorder tool calls, perturb arguments, inject tool errors, alter world effects, truncate output), evaluates the suite's deterministic contracts on every mutant at no model cost, and reports a mutation score, each surviving mutant and a verified contract that would kill it. The mutant set is identical across runs for a fixed seed. `--json` validates against `cli/mutate.schema.json`; exit 0 report produced, 1 no contract applies to any fixture, 2 usage or load error. See `docs/guides/mutation.md`.
- `ATTEST_CONTRACTS_DIR` makes each engine process record the assertion specs it evaluates (one `0600` NDJSON file per process, no traces or outputs) so `attest mutate --contracts` can replay a suite's contracts. `evaluate_batch` takes an optional `context: {test_id, file, line}` that the pytest plugin and the Vitest integration send for the running test; the engine records it next to each spec and does not otherwise use it.
- The policy rule `min_mutation_score: {score, severity}` with `attest policy evaluate --mutation <mutate.json>`: a score below the threshold exits 1 (`block`) or 2 (`warn`). A policy with the rule and no `--mutation` is a load error (exit 3).
- `attest guide [<topic>]` prints the shipped guides (`docs/guides/*.md`) from the engine binary, offline; `--json` gives `{topics:[{name,title}]}` or `{topic,title,markdown}`. New guides: `cli` (agent-facing CLI reference, exit-code table) and `agents` (skills, `init --skills`, plugins). Guides are staged into the engine module by `go generate ./internal/assets/` (`make generate`) and embedded; a test in `go test ./...` fails when the staged copies drift.
- Every engine command and subcommand accepts `--json` and then writes exactly one JSON document to stdout on success; on a usage, load or runtime error it writes nothing to stdout and the exit code plus stderr describe the error. Schemas are in `proto/attest/v1/cli/` (`doctor` keeps `proto/attest/v1/doctor.schema.json`) with valid and invalid fixtures in `protocol-tests/cli/`, tested in Go, Python and TypeScript. `capture`, `replay` and `world` with `-- <command>` send the command's stdout to stderr under `--json` and print one result object that carries its `exit_code`. `attest help --json` lists every command with its schema and exit codes. `attest init --json` (both SDKs) prints `{created, updated, skipped}`, and the SDKs add `init` to `attest help --json`.
- Each command declares its exit codes in the registry; the table in `docs/guides/cli.md` is checked against it by a test.
- `attest skills list|install [--dir D] [--json]` writes the embedded `attest` and `attest-verify-generator` skills to `.agents/skills/` (Codex, Cursor) and `.claude/skills/` (Claude Code), touching only attest-owned files. It validates every destination before writing, follows directory symlinks that resolve inside `--dir` (writing each real file once) and refuses symlinks that resolve outside it or symlinked skill files, writing nothing. `attest init --skills` (Python and TypeScript, alone or with `--ci github`) calls it. The `attest` skill routes to writing contracts, capturing an incident, debugging a failure, CI setup and migrating from Promptfoo or DeepEval with a `MIGRATION.md` progress file; `attest-verify-generator` interviews a repository and writes `verify-<agent>` skills.
- `attest mcp [--root D]` serves attest to coding agents over MCP on stdio with a fixed toolset: `trace_check`, `evaluate`, `explain_failure`, `draft_contract`, `run_contract` and `fixture_info`. It is read-only and offline (deterministic layers only; judge and embedding assertions return `not_evaluated` with `provider_not_configured`) and confined to the project root: paths are resolved through symlinks, files are capped at 32 MiB. `run_contract` parses statements such as `tool_cardinality("issue_refund", per="order_id", at_most=1)` with a closed grammar, never as code, and returns per-statement results plus the Python and TypeScript test lines. `attest mcp --tools --json` prints the tool list; `attest mcp install --dir D --command <json argv>` and `attest init --mcp` (Python and TypeScript) write `.mcp.json`, `.cursor/mcp.json` and `.codex/config.toml` without touching other servers. Guide: `mcp`.
- Claude Code, Cursor and Codex plugin manifests (`.claude-plugin/`, `.cursor-plugin/`, `.codex-plugin/`) publish the same `skills/`; they are generated from `VERSION` and one metadata source by `go generate` and checked for drift. In Claude Code: `/plugin marketplace add attest-framework/attest`.

### Security

- JSON Schema assertions (`type: schema`) resolve `$ref` inside the schema document only: `file://` and `http://` references no longer load, for every engine caller. Previously a schema could read any local file the engine could, and its compile errors could echo part of it. The compiled-schema cache is bounded.

### Changed

- `attest policy evaluate` exits 3 on usage errors and when the policy, report or baseline fails to load (previously 2 and 1), so they cannot be read as a warning or a block. Verdicts keep 0 pass, 1 block, 2 warn.
- `attest cache`, `baseline`, `version` and `policy evaluate` reject unexpected arguments; `cache` usage errors exit 2 (previously 1).
- `attest capture --refresh` also refuses to prompt (exit 2, `--yes` hint) when `CI` is set or `--json` is given.

## [1.0.0-rc.3] — 2026-11-03

Third release candidate, themed "Flight recorder": capture a real run or incident as a verified fixture, replay the agent's real code against it in CI with zero model calls, check what the agent did to a simulated world with effect contracts, and get a pull-request summary, inline annotations, JUnit and diff-based test selection.

### Added

- `attest capture --name <name> -- <command>` turns a run into a regression fixture. The engine starts a loopback listener hosting a recording proxy for OpenAI Chat Completions, OpenAI Responses and Anthropic Messages (streaming and not) and an OTLP/HTTP trace receiver (`http/protobuf` and `http/json`, optionally gzip), runs the command with `OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL` and `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` pointing at it, forwards model calls to the upstreams set by those variables in the capture's own environment (default `https://api.openai.com/v1` and `https://api.anthropic.com`), and writes `tests/attest/fixtures/<name>.attest.json` (`--out-dir` to change). Request headers are never recorded; values of environment variables whose names end in `KEY`, `TOKEN`, `SECRET` or `PASSWORD` and key-shaped strings (`sk-…`, `AKIA…`, `ghp_…`, bearer credentials, private keys) are replaced by `[redacted]` in keys and values. Trace and span ids, provider response and tool-call ids, timestamps, durations and emails are templated to stable placeholders across the trace, the exchanges and their replay keys, so capturing the same scripted run twice writes the same bytes (timestamps, numeric or ISO-8601, are numbered by position, never by value: the fixture is walked with each object's start-like keys first and end-like keys last, so every start precedes its end and a parent brackets its children, and runs that differ only in timing write the same bytes). A model call the agent cancels or does not read to the end, and an OTLP export request still in flight at the end or rejected, fail the capture, since the fixture could not replay the run. SIGINT and SIGTERM sent to `attest capture` are forwarded to the command. `--from <otlp.json>` builds a trace-only fixture (not replay-capable) from an export; `--serve` hosts the listeners for in-process helpers. `attest capture --check-redaction <fixture>` exits 1, naming the JSON path, when a fixture holds a secret.
- Capture drafts deterministic contracts from the run as ceilings on what it observed (the tools called, the order of their first calls, per-id cardinality for each tool called with an `id` / `*_id` argument, the structured output's shape, and a cost ceiling at 1.5× the observed cost) and marks the fixture verified only when they all pass and the captured trace passes trace check; otherwise it exits 1 naming the reason. A normal capture drafts each tool's cardinality at the observed per-id maximum, so a healthy run that reads an id twice verifies. `--expect-fail` saves an incident reproduction: cardinality is drafted at one call per id, the fixture is verified when drafted contracts fail, and those are recorded as expected failures (INC-1042: `cardinality-issue_refund-per-order_id`).
- Python capture helpers: `with attest.record("<name>", expect_fail=...)` captures the code in the block by starting `attest-engine capture --serve` and setting `OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL` and `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` for the block (construct model clients and the OpenTelemetry exporter inside it; the global tracer provider is flushed on exit), and raises `attest.CaptureError` when the fixture is not verified. Captures cannot overlap (`record()` sets process-wide variables). `attest.load_fixture(path)` reads a fixture. For a verified fixture, `attest capture` and `record()` draft `tests/attest/test_<name>.py` (name lowercased, other characters as `_`) from its contracts with the `expect()` DSL, unless that file exists: a verified fixture's test enforces them with `attest.evaluate`, an incident reproduction's test checks with `attest.inspect` that the expected failures still fail.
- TypeScript capture helpers: `record(name, fn, { expectFail, flush })` from `@attest-ai/core` captures what `fn` does through `attest-engine capture --serve` (pass the tracer provider's `forceFlush` as `flush`) and throws `CaptureError` when the fixture is not verified; `loadFixture(path)` reads a fixture. For a verified fixture, `npx attest capture` and `record()` draft `tests/attest/<name>.test.ts` with `attestExpect` and `inspect` / `evaluate` from `@attest-ai/vitest`, unless that file exists. Captures cannot overlap. Every drafted contract has a TypeScript DSL call. See `docs/guides/capture.md`.
- Fixture schema v1 (`proto/attest/v1/fixture-v1.schema.json`, fixtures in `protocol-tests/fixture-v1/`): the trace-v2 trace, the exchanges with a replay key (model, parameters, prompt hash, tool-schema hash), the drafted contracts, their verification and a `world` section for the simulated world's final state (`null` until `--world` fills it). `attest trace check` accepts a fixture.
- `attest-engine replay --name <name> -- <command>` serves a fixture's recorded model responses to the real agent: the engine's proxy answers every OpenAI Chat Completions, OpenAI Responses and Anthropic Messages request from the fixture (streamed exchanges replay as events) and makes no provider call. Requests are matched on model, parameters, prompt and tool schemas after the capture redactor is applied to the live request and every volatile value (ids, timestamps, emails, and the `{{id_N}}` placeholders a replayed response hands back to the agent) is renumbered by first occurrence within that request, so a live request needs no placeholder numbering of its own; identical retries consume successive recordings in order. `--mode strict` (default) answers a request with no recording with an error naming the first component that changed, for example `replay miss: system prompt changed (hash a1b2c3d→e4f5a6b); re-record with \`attest capture --refresh inc-1042\``; `--mode auto` forwards it live only when the request carries exactly the `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` of the engine's environment for that provider (the proxy adds no credentials), and otherwise reports the miss; `--no-live` refuses every live forward. A model name in a miss message is quoted. `replay --serve` is the long-lived form for SDK helpers (environment line, `stop`, summary line with replayed, live and missed requests and the cost of live calls the built-in price table covers).
- `attest capture --refresh <name>` re-runs the fixture's recorded command with its original flags (`--expect-fail` included) and overwrites the fixture. The command comes from the fixture file, so it is printed shell-quoted first and runs only after `--yes` or a `y` at a terminal (otherwise exit 2, nothing runs); it refuses when the command's arguments were redacted or templated, or the fixture came from `--from` or an in-process capture.
- Report-v2 gains optional replay provenance: `tests[].replay` (`fixtures`, `mode`, `replayed`, `live`, `misses`, `miss`) and `summary.replay` (`replayed`, `live`, `misses`, `live_cost_usd`, `unpriced_live_calls`). `summary.usage` keeps counting only attest-initiated model calls. Valid and invalid examples are in `protocol-tests/report-v2/`.
- `replay_miss` joins the `not_evaluated` reasons (`ReasonReplayMiss` in Go, `proto/attest/v1/report-v2.schema.json`, `protocol-tests/verdicts/verdict.schema.json`): a fixture-bound test in `auto` replay mode without a provider key reports it. The SDKs set it; the engine's own batches never return it.
- Python replay: the `attest_replay("<name>")` pytest fixture (and `attest.start_replay()`) runs the agent's real code against a fixture's recorded model responses. `--attest-replay=strict|auto|off` (also `ATTEST_REPLAY`; default `strict` when `CI` is set, `auto` otherwise) chooses what a request with no recording does: `strict` fails the test with the engine's miss message, naming the changed component and the `attest capture --refresh` command, whatever the agent raised and whatever marker the test carries (`xfail(strict=True)` and Vitest `it.fails` cannot turn it into an expected failure); `auto` goes live when a provider key is set and otherwise skips the test as `not_evaluated: replay_miss` (a failure under `--attest-strict`). The terminal summary prints `attest replay: N replayed · M live model calls · $X.XX`, and `--attest-report` writes the per-test and summary replay provenance. See `docs/guides/replay.md`.
- TypeScript replay: `@attest-ai/core` adds `startReplay()` and `@attest-ai/vitest` adds `useReplay(name, { mode, egress, strict })`, with the same modes (`ATTEST_REPLAY=strict|auto|off`) and outcomes. `AttestReporter` writes the replay provenance and prints the same summary line.
- Provider-egress guard: `pytest --attest-egress=block`, `ATTEST_EGRESS=block` or `useReplay(name, { egress: "block" })` fails in-process connections to `api.openai.com`, `api.anthropic.com`, `generativelanguage.googleapis.com` and `*.openai.azure.com`; loopback is always allowed. It is off by default. With the guard on, replay runs the engine with `--no-live`, so no miss goes live. Control and format characters in miss and engine-error text are escaped before they reach a test report.
- Attest's own L6 judge integration tests (`sdks/python/tests/integration/test_judge_replay.py`) run `passes_judge` through `ATTEST_JUDGE_BASE_URL` pointed at the replay proxy, from fixtures recorded against a scripted OpenAI-compatible judge. `make record-judge-fixtures` re-records them against a real provider; it needs `ATTEST_JUDGE_API_KEY` (or `OPENAI_API_KEY`) and `ATTEST_JUDGE_MODEL` and refuses to run without them.
- `attest-engine world --file world.yaml -- <command>` serves the external system an agent acts on as a local stateful HTTP fake. `world.yaml` (schema `proto/attest/v1/world-v1.schema.json`, fixtures in `protocol-tests/world-v1/`) declares JSON resources with create, list and get routes, an idempotency-key header for creates, seed records and a fault script, and names the environment variable that carries the world's base URL (for example `PAYMENTS_BASE_URL`). A repeated idempotency key replays the first response and commits nothing. Faults: `timeout_after_commit` commits the create, then holds the response until the client disconnects (30 s cap), so the outcome never depends on a wall-clock race; `429` and `500` fire on listed request ordinals or by probability from a seeded generator, and the same seed and request sequence give the same outcomes. A malformed file exits 2 naming the JSON path (`/resources/refunds/routes/1`). `--serve` prints `{"env":...}`, serves until `stop`, then prints `{"world":<final snapshot>}`; `--state-out` writes the snapshot. `GET <base URL>/_attest/state` returns the current state as a world-v1 snapshot.
- `attest capture --world world.yaml` and `attest-engine replay --world world.yaml` host the world in the same session as the recording or replaying proxy. Capture stores the world's final state (`world_version`, definition hash, state, faults fired and their count) in the fixture's `world` section, redacted like the rest of the fixture, and email addresses in record keys and values are templated to `{{email_N}}` with the same numbering as the trace (one address, one token across the fixture; ids and timestamps in records are kept as the world held them). The section is evidence only: `replay --world` starts a fresh world from `world.yaml`, not from the fixture. A capture without `--world` keeps `null`. `attest capture --refresh` refuses a fixture that carries a world section, since a refresh would drop it.
- An over-long request line (over 10 MiB) no longer kills the engine: it is answered with a `-32600` `REQUEST_TOO_LARGE` error carrying the request's id (null when the id is not in the first bytes) and the engine keeps serving.
- `effect` assertions (Layer 3, deterministic, no new layer) check what the agent did to the world: `count` of matching records (`equals`, `at_most`, `at_least`), `unique` by fields, and `idempotent` keys. The spec embeds a world-v1 snapshot as its evidence, so the evaluator interface is unchanged. A violation reads, for example, `effect violated: 2 refunds for O-1042 (expected 1)`. A `null` snapshot is `not_evaluated` with reason `missing_evidence`. See `proto/attest/v1/protocol-spec.md`.
- Python world fixtures: `attest.start_world(path)` (and the `attest_world(path)` pytest fixture) hosts a `world.yaml` through `attest-engine world --serve` and sets its base URL variable (`PAYMENTS_BASE_URL`) until the test ends; the session has `env`, `base_url`, `state()` (the current world-v1 snapshot, from `GET <base URL>/_attest/state`) and `stop()` (the final snapshot). It composes with `attest_replay` in one test (two engine processes, merged environment). `attest.record(..., world=path)` captures with `--world`. `expect(trace).world(snapshot)` adds `count(resource, equals=, at_most=, at_least=, **where)`, `unique(resource, by=, **where)` and `idempotent(resource, key=, **where)` effect checks; `world(None)` is `not_evaluated` (`missing_evidence`).
- TypeScript world fixtures: `startWorld(path)` from `@attest-ai/core` and `useWorld(path)` from `@attest-ai/vitest` (composes with `useReplay`) with the same session, `record(name, fn, { world })`, and `attestExpect(trace).world(snapshot)` with `count(resource, { equals, atMost, atLeast, where })`, `unique(resource, { by, where })` and `idempotent(resource, { key, where })`. Both SDKs emit the same specs; `protocol-tests/contracts/effect-dsl.json` pins them and the engine's result for each.
- `docs/guides/world-fixtures.md`: the world model, the `world.yaml` reference with the INC-1042 payments example, faults and determinism, idempotency semantics, running a world, the effect DSL, composing with replay, and limits.
- Effect checks guard the request size: `expect(...).world(snapshot)` measures the snapshot once and raises `ValueError` / `Error` before sending when the checks of one chain would embed it more than 9 MiB in total (`MAX_WORLD_BYTES`), since the engine limits a request line to 10 MiB. The Python codec sends UTF-8 instead of `\uXXXX` escapes, so non-ASCII text no longer inflates up to 6x. In Python `resource` is positional-only and filters can be given as a `where` dict (use it for fields named `key`, `by` or `equals`); a filter given both ways raises `TypeError`. TypeScript `layerForType("effect")` returns 3, like the engine.
- `attest report summary --report <path>... [--policy-result <file>]` prints the pull-request summary as Markdown: `### attest: N passed · N failed · N not evaluated` (plus ` · N skipped` for plain skips and expected failures), `N replayed · N live model calls · $X`, the usage line with retry cost, not-evaluated counts by reason, a capped table of failures and replay misses with `file:line`, and the policy verdict. Several reports merge; unreadable input exits 2.
- `attest report junit --report <path>... --out <file>` writes JUnit XML from report-v2 (one suite per test file, `file` and `line` on each case, `<failure>` for failed tests, `<skipped>` for skipped tests, with the reasons when their assertions were not evaluated). `attest report rows --report <path>... --json` prints the failures and replay misses as `{file, line, test_id, assertion_id, type, status, reason, explanation}` rows. A strict-mode replay miss, which fails its test without an assertion row, appears as a `replay_miss` row.
- `attest select --base <ref> [--json] [--repo <dir>]` lists the tests a diff needs: touched test files, and the tests that reference a touched fixture by capture name. Lockfiles, dependency manifests, attest config, world files and any file it does not recognize select `all`; markdown selects nothing. Exit 2 when git or the ref fails.
- Guides `docs/guides/baselines.md`, `policies.md` and `selection.md`.
- `pr-annotate` v2 (`.github/actions/pr-annotate`, ships in rc.3): `report` accepts several paths (newline or space separated) and the policy runs on each, the worst exit winning (an evaluator error, then 1, 2, 0). It posts one summary comment per pull request, rendered by `attest report summary` plus a link to the run, found by the hidden marker `<!-- attest:pr-annotate -->` and updated in place; only marker comments written by the token's own login are updated (the Actions bot, or the user for a personal token), so a planted marker is ignored. The summary is always written to the job summary too. The comment is posted only on same-repository `pull_request` events; fork pull requests and tokens that cannot write get the job summary and a notice or warning, never a failed step. Failures and replay misses from `attest report rows` become `::error` (hard failures, replay misses) and `::warning` (soft failures, not-evaluated) annotations at the test's file and line, relative to the repository root (a row's `file` is prefixed with its report's directory), escaped per the workflow-command rules, with control characters stripped, messages capped, and at most 10 errors and 9 warnings per step (one annotation counts the rest). The engine never receives the token, the policy result is written under `RUNNER_TEMP` and not echoed, and `##[` is broken up in every logged string. The engine is found as `engine-binary`, then `ATTEST_ENGINE_PATH`, then `PATH`, then `~/.attest/bin/<engine-version>/attest-engine` (`engine-version` defaults to the action's own `VERSION`). The optional `comment-author` input names the login whose comment is updated (`<app-slug>[bot]` for a GitHub App token, which cannot look itself up). Inputs and the `exit-code` output are unchanged. See `docs/guides/annotations.md`.
- `attest init --ci github` writes an `attest report junit` step and uploads `attest-junit.xml` with the report, and sets `permissions` to `contents: read` for the workflow with `pull-requests: write` on the job only (both SDKs).

### Changed

- Behavior change: `pr-annotate` no longer posts pull request review comments (they could only target lines in the diff). Failures are workflow annotations and the summary is one updated comment; the action now runs `attest-engine report summary` and `report rows`, so the engine is required even without a `policy`. A caller pinned to `@v1.0.0-rc.1` or `@v1.0.0-rc.2` is unaffected until it moves to the rc.3 tag.
- `attest report summary` renders every report-derived string (test ids, file paths, explanations, not-evaluated reasons, policy rules and details) as an inline code span, with control, bidi and zero-width characters removed and the fence longer than any backtick run in the text, so agent output in a pull request comment cannot ping users, link issues, inject HTML or hide the rest of the comment.
- The engine builds with Go 1.27 (`engine/go.mod` `go 1.27.0`; CI and the release cross-compile use Go 1.27). Go 1.25 is outside Go's two-release support window and no longer gets security fixes. Prebuilt engine binaries are unaffected; building from source, including through the `setup-attest` action (which reads the version from `engine/go.mod`), needs Go 1.27 or later.
- The Python and TypeScript SDK workflows build the engine (`make engine`, Go 1.27) before their tests and run when `engine/**` changes, so the engine-backed integration tests run in CI instead of skipping.

### Security

- The capture listener binds `127.0.0.1` on an ephemeral port and serves only under a random 128-bit path token handed to the command in its environment (other paths are 404 and nothing is recorded), refuses requests whose `Host` is not a loopback name or that carry an `Origin` header, admits 64 concurrent requests, bounds request and recorded response bodies at 32 MiB and a session's recordings at 512 MiB (the capture fails past it), checks OTLP protobuf span, attribute, value and nesting limits on the wire before decoding it, and closes when the command exits. Provider credentials pass through to the upstream in memory only. Redaction covers secrets streamed across several response deltas (Chat Completions content, refusal, reasoning content, audio transcript and function or tool-call arguments; every Responses `*.delta` event; Anthropic text, `partial_json`, thinking and citations), which become `[redacted]` in the first delta. A response is a stream when its media type is `text/event-stream` (any parameters) or the request asked for one; a stream that does not parse fails the capture rather than being written. It also covers environment names ending in `_PASS`, `_PWD`, `PASSWD`, `CREDENTIALS`, `CREDENTIAL`, `DSN` or `AUTH` (not `PWD`, `OLDPWD` or `*_ASKPASS`), the password of any URL with userinfo, Stripe, Groq, Hugging Face and xAI keys, percent-decoded exchange paths with `key`, `api_key`, `apikey`, `token`, `access_token`, `sig` and `signature` query values, the values of secret-named command flags and of `NAME=value` arguments whose name marks a secret, and secrets an in-process block loads into its environment. Names from the trace are escaped in terminal output.
- The replay listener is the capture listener in another mode: `127.0.0.1` on an ephemeral port under a random 128-bit path token, loopback `Host` only, no `Origin`, 64 concurrent requests. A fixture is read with a 256 MiB size limit and refused when it has more than 10,000 exchanges, a truncated or empty exchange, an unknown provider or a status outside 100–599. Live requests are redacted before matching, so a secret in a request never reaches a miss message or a summary, and replay answers a miss with HTTP 422, which the official SDKs do not retry.
- The world listener is the capture listener: `127.0.0.1` on an ephemeral port under a random 128-bit path token, loopback `Host` only, no `Origin`, 64 concurrent requests. `world.yaml` is untrusted input: at most 256 KiB, one YAML document, string keys only, no unquoted timestamps, validated against world-v1 before it is used. The world refuses request bodies over 1 MiB, holds at most 10,000 records and 1 MiB of state, faults listed included (`507` beyond), lists at most 1,000 fired faults in a snapshot (`faults_fired_count` stays exact), rejects integers beyond 2^53-1 and non-finite numbers in request bodies and in `world.yaml`, accepts only the query filters a resource declares, and releases held responses when the session closes.

### Fixed

- `@attest-ai/vitest` declares `vitest >=3.0.0 <5`. Vitest 5 removed `vitest/suite`, which the plugin imports, so the old `>=3.0.0` range accepted a Vitest it cannot load. The install commands in the README, `docs/guides/behavior-contracts.md` and `docs/guides/merge-gate.md` now pin `vitest@^4`, since a bare `vitest` installs Vitest 5. Vitest 5 support is planned.
- TypeScript `OTelAdapter.fromOtlp` / `fromSpans` no longer hang for 60 s and throw `trace convert timed out` under parallel load. They ran the engine with `spawnSync` and the export on stdin, which on macOS occasionally never returned: about 1 call in 100 with 8 concurrent callers. The engine now runs as an asynchronous child process, which also stops the conversion from blocking the event loop.

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

[Unreleased]: https://github.com/attest-framework/attest/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/attest-framework/attest/releases/tag/v1.1.0
[1.1.0-rc.5]: https://github.com/attest-framework/attest/releases/tag/v1.1.0-rc.5
[1.1.0-rc.4]: https://github.com/attest-framework/attest/releases/tag/v1.1.0-rc.4
[1.1.0-rc.3]: https://github.com/attest-framework/attest/releases/tag/v1.1.0-rc.3
[1.1.0-rc.2]: https://github.com/attest-framework/attest/releases/tag/v1.1.0-rc.2
[1.1.0-rc.1]: https://github.com/attest-framework/attest/releases/tag/v1.1.0-rc.1
[1.0.1]: https://github.com/attest-framework/attest/releases/tag/v1.0.1
[1.0.0]: https://github.com/attest-framework/attest/releases/tag/v1.0.0
[1.0.0-rc.5]: https://github.com/attest-framework/attest/releases/tag/v1.0.0-rc.5
[1.0.0-rc.4]: https://github.com/attest-framework/attest/releases/tag/v1.0.0-rc.4
[1.0.0-rc.3]: https://github.com/attest-framework/attest/releases/tag/v1.0.0-rc.3
[1.0.0-rc.2]: https://github.com/attest-framework/attest/releases/tag/v1.0.0-rc.2
[1.0.0-rc.1]: https://github.com/attest-framework/attest/releases/tag/v1.0.0-rc.1
[1.0.0-rc]: https://github.com/attest-framework/attest/releases/tag/v1.0.0-rc
[0.9.0]: https://github.com/attest-framework/attest/releases/tag/v0.9.0
[0.8.0]: https://github.com/attest-framework/attest/releases/tag/v0.8.0
[0.7.0]: https://github.com/attest-framework/attest/compare/v0.6.1...v0.7.0
