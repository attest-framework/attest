# Changelog

All notable changes to Attest are documented here. Versions follow [Semantic Versioning](https://semver.org/).

---

## v0.4.1 — 2026-02-21

**SDK patch release — engine auto-download**

### Features

- **Engine auto-download** — Both Python and TypeScript SDKs now automatically download the `attest-engine` binary from GitHub Releases on first use. No manual binary setup required after `uv add attest-ai` or `pnpm add @attest-ai/core`.
- **SHA256 verification** — Downloaded binaries are verified against `checksums-sha256.txt` from the release. Checksum mismatch aborts the download with a clear error.
- **Version-pinned cache** — Binaries are cached at `~/.attest/bin/` with a `.engine-version` marker. SDK version mismatch triggers automatic re-download.
- **Discovery chain** — Engine binary resolution follows a predictable order:

    ```
    ATTEST_ENGINE_PATH env var
      → PATH lookup
      → ~/.attest/bin/ (shared cache, version-checked)
      → ../../bin/ (monorepo dev layout)
      → ./bin/ (local)
      → auto-download from GitHub Releases
      → actionable error message
    ```

- **Opt-out** — Set `ATTEST_ENGINE_NO_DOWNLOAD=1` to disable network access. The error message explains alternative installation methods.

### Fixes

- **pytest plugin** — `pytest.skip()` replaced with `pytest.fail()` when the engine binary is missing. With auto-download in place, silent skipping is no longer appropriate; real errors are now surfaced.
- **TypeScript VERSION** — Corrected from `0.3.0` to `0.4.1`.

### Other

- **Examples moved** — All example projects relocated to [`attest-framework/attest-examples`](https://github.com/attest-framework/attest-examples) for independent versioning. A redirect README remains in the main repo.

### Install / Upgrade

=== "Python"

    ```bash
    uv add attest-ai
    ```

=== "TypeScript"

    ```bash
    pnpm add @attest-ai/core
    ```

### Environment Variables

| Variable | Purpose | Default |
|----------|---------|---------|
| `ATTEST_ENGINE_PATH` | Absolute path to engine binary — skips all discovery | unset |
| `ATTEST_ENGINE_NO_DOWNLOAD` | `1` / `true` / `yes` disables auto-download | unset (enabled) |

---

## v0.4.0 — 2026-02-20

**Production & Polish**

- Result history with SQLite storage
- Drift detection (σ-based statistical thresholds)
- Continuous eval runner with sampling and alerting
- Plugin system (`attest.plugins` entry point group)
- CrewAI adapter (11 adapters total)
- CLI `init` and `validate` commands
- MkDocs documentation site

---

## v0.3.0 — 2026-02-20

**Simulation & Multi-Agent**

- Layers 7-8: simulation runtime, multi-agent testing
- TypeScript SDK (first npm publish: `@attest-ai/core`, `@attest-ai/vitest`)
- Framework adapters: LangChain, Google ADK, LlamaIndex

---

## v0.2.0 — 2026-02-19

**Semantic & Judge Layers**

- Layers 5-6: ONNX local embeddings, LLM-as-judge
- Soft failure support
- OTel adapter
- `setup-attest` GitHub Action

---

## v0.1.0

**Foundation**

- Layers 1-4: schema validation, cost/performance, trace structure, content validation
- Python SDK with pytest plugin
- 4 provider adapters: OpenAI, Anthropic, Gemini, Ollama
- PyPI + GitHub release
