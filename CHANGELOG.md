# Changelog

All notable changes to Attest are documented in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/). One `VERSION` covers the engine, the Python SDK (`attest-ai`) and the TypeScript SDKs (`@attest-ai/core`, `@attest-ai/vitest`); PyPI shows pre-releases in PEP 440 form (`1.0.0-rc.1` → `1.0.0rc1`).

## [Unreleased]

### Added

- `release.yml` publishes a release from one tag push: it builds the six engine binaries, checksums, the Python distributions and the npm tarballs once, verifies them, and after `release` environment approval publishes the GitHub (pre-)release, then PyPI (trusted publishing) and npm (trusted publishing with provenance) from those stored artifacts, records which components published in the release notes, and dispatches the website sync. A tag that differs from `VERSION` fails before anything builds. `workflow_dispatch` with `dry_run: true` runs the whole pipeline without publishing.
- `setup-attest` accepts a `ref` input: consumer repositories build the engine and install the Python and TypeScript SDKs from any attest branch, tag or commit.
- `CHANGELOG.md` is the single changelog; `scripts/release-notes.sh <version>` prints one version's section.

### Removed

- `docs/changelog.md`, superseded by this file.

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
