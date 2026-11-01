# Changelog

All notable changes to Attest are documented in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/). One `VERSION` covers the engine, the Python SDK (`attest-ai`) and the TypeScript SDKs (`@attest-ai/core`, `@attest-ai/vitest`); PyPI shows pre-releases in PEP 440 form (`1.0.0-rc.1` → `1.0.0rc1`).

## [Unreleased]

### Added

- `release.yml` publishes a release from one tag push: it builds the six engine binaries, checksums, the Python distributions and the npm tarballs once, verifies them, and after `release` environment approval publishes the GitHub (pre-)release, then PyPI (trusted publishing) and npm (trusted publishing with provenance) from those stored artifacts, records which components published in the release notes, and dispatches the website sync. A tag that differs from `VERSION` fails before anything builds. `workflow_dispatch` with `dry_run: true` runs the whole pipeline without publishing.
- `setup-attest` accepts a `ref` input: consumer repositories build the engine and install the Python and TypeScript SDKs from any attest branch, tag or commit.
- `CHANGELOG.md` is the single changelog; `scripts/release-notes.sh <version>` prints one version's section.
- `attest-engine doctor [--json]` reports the engine and protocol versions, the capabilities `initialize` would advertise, the judge provider, model, base URL and key source (never the key), the embedding provider, simulation mode and the cache directory. JSON follows `proto/attest/v1/doctor.schema.json`. A configuration error exits 1 with the server's startup message; a plain-http judge endpoint on a non-loopback host is flagged as insecure.
- `ATTEST_JUDGE_BASE_URL` points the LLM judge at any OpenAI-compatible endpoint. `ATTEST_JUDGE_API_KEY` overrides `OPENAI_API_KEY` for the judge. See `docs/guides/configuration.md`.
- `attest-engine --help` lists every command; each command is one file registered through a command registry.
- `attest` console script for the Python SDK (`uv run attest`), matching `npx attest`. Both implement `attest init` and forward every other command (`doctor`, `baseline`, `policy`, `cache`, `calibrate`, `version`) to a version-checked engine with arguments, output and exit code unchanged. A forwarded command must be listed by the engine's `help`; an engine that predates it (such as the `v1.0.0-rc` prerelease, which reports the same version but has no command registry) exits 1 before running. `attest --help` and bare `attest` always exit 0 and never download: they list `init`, plus the engine's commands when a matching engine is already available through `ATTEST_ENGINE_PATH`, `PATH` or the versioned cache, and otherwise print how to get one.
- `EngineVersionMismatchError` (Python `attest.exceptions`, TypeScript `engine-manager`) names the engine path, where discovery found it, the version it reports and the version required.

### Changed

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

[Unreleased]: https://github.com/attest-framework/attest/compare/v1.0.0-rc...HEAD
[1.0.0-rc]: https://github.com/attest-framework/attest/releases/tag/v1.0.0-rc
[0.9.0]: https://github.com/attest-framework/attest/releases/tag/v0.9.0
[0.8.0]: https://github.com/attest-framework/attest/releases/tag/v0.8.0
[0.7.0]: https://github.com/attest-framework/attest/compare/v0.6.1...v0.7.0
