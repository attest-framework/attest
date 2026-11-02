# Engine configuration

The engine reads its configuration from environment variables when it starts. An invalid provider, URL or integer setting stops the engine with an error naming the variable. Two exceptions keep their historical behavior: `ATTEST_JUDGE_TIMEOUT_S` falls back to 30 on a non-positive or non-integer value, and `ATTEST_JUDGE_META_EVAL` is enabled only by the exact value `true`.

## Engine discovery and the `attest` command

Each SDK release requires one engine version, `ENGINE_VERSION` (`1.0.0-rc.N` during the release candidates). The SDK looks for an engine in this order and uses the first one it finds:

1. `--attest-engine` (pytest) or the `engine_path` / `enginePath` argument of `EngineManager`
2. `ATTEST_ENGINE_PATH`
3. `attest-engine` (`attest-engine.exe` on Windows) in a `PATH` directory; empty entries are skipped, so the current directory is never searched
4. `~/.attest/bin/<ENGINE_VERSION>/attest-engine`
5. A download of `ENGINE_VERSION` from GitHub Releases into that directory, verified against the release's `checksums-sha256.txt` and written atomically. `ATTEST_ENGINE_NO_DOWNLOAD=1` turns the download off.

The engine found must report exactly `ENGINE_VERSION` (`attest-engine version`). Otherwise the SDK stops with a version-mismatch error naming the engine, where it came from, the version it reports and the version required. It never skips a mismatched engine in favor of the next location. The pytest fixture, Vitest, `EngineManager` and the `attest` command all use this check. The legacy unversioned `~/.attest/bin/attest-engine` from releases before 1.0 is neither used nor deleted.

The `attest` command (`uv run attest`, `npx attest`) implements `attest init` itself and forwards every other command to the version-checked engine, passing arguments, output and exit code through unchanged:

```bash
attest --help                     # SDK commands, plus engine commands when one is installed
attest --version                  # SDK version and the engine version it requires
attest init                       # scaffold a test project (no engine needed)
attest doctor --json              # = attest-engine doctor --json
attest policy evaluate --policy attest.policy.yaml --report report.json
```

Before forwarding, the CLI checks that the engine's `help` lists the command. If no matching engine is available, or the engine predates the command, the command exits 1 before anything runs, so `attest policy evaluate` never exits 0 without evaluating. A command killed by a signal exits 128 + N. `attest --help` never downloads: it lists the engine's commands only when a matching engine is already available, and always exits 0. Run tests with `pytest` or `vitest` directly.

## Check your setup

`attest doctor` (or `attest-engine doctor`) reports what the engine would start with under the current environment: engine and protocol versions, the capabilities `initialize` advertises, the judge and embedding providers, simulation mode and the cache directory.

```bash
attest doctor          # human-readable
attest doctor --json   # machine-readable, schema: proto/attest/v1/doctor.schema.json
```

The output never contains an API key. It shows only which variable the judge key came from. A configuration error exits 1 with the same message the server would fail to start with. In `onnx` builds that select ONNX embeddings, `doctor` constructs the embedder just as the server does, so the first run downloads the model.

`attest-engine --help` lists every command. An unknown command exits 2.

## LLM judge (Layer 6)

The judge runs natively on OpenAI, Anthropic and Gemini, and on any OpenAI-compatible endpoint (vLLM, Ollama's `/v1` API, LiteLLM, Azure-compatible gateways). Provider setup, rate limits and calibration per model: [Judges](judges.md).

| Variable | Purpose | Default |
|---|---|---|
| `ATTEST_JUDGE_PROVIDER` | `openai` (also OpenAI-compatible endpoints), `anthropic` or `gemini`. Any other value is a startup error. | `openai` |
| `ATTEST_JUDGE_API_KEY` | Judge API key. Takes precedence over the provider's standard key variable, and is required with `ATTEST_JUDGE_BASE_URL`. For an endpoint without authentication, set any non-empty value. | unset |
| `OPENAI_API_KEY` | OpenAI's standard key variable. Used for the `openai` judge when `ATTEST_JUDGE_API_KEY` is unset and no `ATTEST_JUDGE_BASE_URL` is set, and for OpenAI embeddings. Never sent to a custom base URL. | unset |
| `ANTHROPIC_API_KEY` | Anthropic's standard key variable, used for the `anthropic` judge under the same rules. | unset |
| `GEMINI_API_KEY`, `GOOGLE_API_KEY` | Gemini's standard key variables (in that order), used for the `gemini` judge under the same rules. | unset |
| `ATTEST_JUDGE_BASE_URL` | Base URL of the selected provider's API, e.g. `http://localhost:11434/v1` for an OpenAI-compatible server or an Anthropic/Gemini gateway root. Requires `ATTEST_JUDGE_API_KEY`. Must be `http`/`https` without credentials, query or fragment; use `https` for anything other than a local endpoint, because the key is sent with every request. | `https://api.openai.com/v1`, `https://api.anthropic.com`, `https://generativelanguage.googleapis.com` |
| `ATTEST_JUDGE_MODEL` | Judge model name. Required for `anthropic` and `gemini`. | `gpt-4.1` for `openai` |
| `ATTEST_JUDGE_TIMEOUT_S` | Per-evaluation judge timeout in seconds. | `30` |
| `ATTEST_JUDGE_META_EVAL` | `true` runs each judge assertion three times and takes the median. | unset |
| `ATTEST_JUDGE_CACHE_MAX_MB` | Judge response cache size, clamped to 10–10000. | `100` |
| `ATTEST_RATE_LIMIT_<PROVIDER>` | Judge requests per minute for the selected provider (`ATTEST_RATE_LIMIT_OPENAI`, `ATTEST_RATE_LIMIT_ANTHROPIC`, `ATTEST_RATE_LIMIT_GEMINI`); must be > 0. | `60` |
| `ATTEST_RATE_LIMIT_<PROVIDER>_BURST` | Judge request burst size for the selected provider; must be > 0. | `10` |

Point the judge at a local OpenAI-compatible server:

```bash
export ATTEST_JUDGE_BASE_URL=http://localhost:11434/v1
export ATTEST_JUDGE_API_KEY=unused
export ATTEST_JUDGE_MODEL=llama3.1
export ATTEST_EMBEDDING_PROVIDER=onnx   # keep Layer 5 local too (onnx builds)
attest-engine doctor
```

`ATTEST_JUDGE_BASE_URL` scopes only the judge. An `OPENAI_API_KEY` left in the environment still enables OpenAI embeddings at the default endpoint, and without `ATTEST_JUDGE_BASE_URL` it enables the paid OpenAI judge too. Unset it, or set `ATTEST_EMBEDDING_PROVIDER=onnx`, for a fully local setup, and check the result with `attest-engine doctor`.

Plain `http` to a host other than `localhost` or a loopback address is allowed for internal servers, but the key travels in cleartext: the engine logs a warning at startup and `doctor` prints `insecure: plain http to non-loopback host`.

## Embeddings (Layer 5)

| Variable | Purpose | Default |
|---|---|---|
| `ATTEST_EMBEDDING_PROVIDER` | `auto` (OpenAI when `OPENAI_API_KEY` is set, else ONNX in `onnx` builds), `openai` or `onnx`. Any other value is a startup error. | `auto` |
| `ATTEST_EMBEDDING_CACHE_MAX_MB` | Embedding cache size. | `500` |
| `ATTEST_ONNX_MODEL_DIR` | Directory for the all-MiniLM-L6-v2 model. The model is downloaded on first use from a pinned revision and rejected unless its sha256 matches; a cached file that does not match is replaced. | `~/.attest/models` |
| `ATTEST_ONNX_RUNTIME_LIB` | Path to the ONNX Runtime 1.26 shared library. When unset, the engine looks in the model directory and standard system library paths. It never downloads the runtime. | unset |

## Evaluation

| Variable | Purpose | Default |
|---|---|---|
| `ATTEST_SIMULATION` | `1`/`true`/`yes`: Layers 5–6 return `not_evaluated(simulation)` and no provider is called. | unset |
| `ATTEST_MAX_SOFT_FAILURES` | Maximum soft failures allowed in one engine session, counted across batches; `0` allows none. Exceeding it fails the batch. | unset (no limit) |
| `ATTEST_EVAL_CONCURRENCY` | Layer 5–6 worker pool size per batch; `0` or a negative value uses the default. | `8` |

## Cache and history

| Variable | Purpose | Default |
|---|---|---|
| `ATTEST_CACHE_DIR` | Cache, baseline and history database directory. | `~/.attest/cache` |
| `ATTEST_HISTORY_MAX_ROWS` | Result history row limit (enables pruning). | `10000` when pruning is enabled |
| `ATTEST_HISTORY_MAX_AGE_DAYS` | Result history age limit in days (enables pruning). | `30` when pruning is enabled |

## Removed variables

These variables are no longer read. Setting them has no effect.

| Removed | Use instead |
|---|---|
| `ATTEST_OPENAI_API_KEY` | `ATTEST_JUDGE_API_KEY` for the judge, `OPENAI_API_KEY` for the judge and OpenAI embeddings |
| `ATTEST_BUDGET_MAX_COST` | `ATTEST_MAX_SOFT_FAILURES` (it always counted soft failures, not cost) |
| `ATTEST_JUDGE_RPM`, `ATTEST_JUDGE_BURST` | `ATTEST_RATE_LIMIT_OPENAI`, `ATTEST_RATE_LIMIT_OPENAI_BURST` |
