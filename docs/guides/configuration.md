# Engine configuration

The engine reads its configuration from environment variables when it starts. An invalid provider, URL or integer setting stops the engine with an error naming the variable. Two exceptions keep their historical behavior: `ATTEST_JUDGE_TIMEOUT_S` falls back to 30 on a non-positive or non-integer value, and `ATTEST_JUDGE_META_EVAL` is enabled only by the exact value `true`.

## Check your setup

`attest-engine doctor` reports what the engine would start with under the current environment: engine and protocol versions, the capabilities `initialize` advertises, the judge and embedding providers, simulation mode and the cache directory.

```bash
attest-engine doctor          # human-readable
attest-engine doctor --json   # machine-readable, schema: proto/attest/v1/doctor.schema.json
```

The output never contains an API key. It shows only which variable the judge key came from. A configuration error exits 1 with the same message the server would fail to start with. In `onnx` builds that select ONNX embeddings, `doctor` constructs the embedder just as the server does, so the first run downloads the model.

`attest-engine --help` lists every command. An unknown command exits 2.

## LLM judge (Layer 6)

The judge works with OpenAI and with any OpenAI-compatible endpoint (vLLM, Ollama's `/v1` API, LiteLLM, Azure-compatible gateways).

| Variable | Purpose | Default |
|---|---|---|
| `ATTEST_JUDGE_API_KEY` | Judge API key. Takes precedence over `OPENAI_API_KEY`, and is required with `ATTEST_JUDGE_BASE_URL`. For an endpoint without authentication, set any non-empty value. | unset |
| `OPENAI_API_KEY` | Provider's standard key variable. Used for the judge when `ATTEST_JUDGE_API_KEY` is unset and no `ATTEST_JUDGE_BASE_URL` is set, and for OpenAI embeddings. Never sent to a custom base URL. | unset |
| `ATTEST_JUDGE_BASE_URL` | OpenAI-compatible base URL, e.g. `http://localhost:11434/v1`. Requires `ATTEST_JUDGE_API_KEY`. Must be `http`/`https` without credentials, query or fragment; use `https` for anything other than a local endpoint, because the key is sent with every request. | `https://api.openai.com/v1` |
| `ATTEST_JUDGE_MODEL` | Judge model name. | `gpt-4.1` |
| `ATTEST_JUDGE_PROVIDER` | Judge provider. Only `openai` is implemented; any other value is a startup error. | `openai` |
| `ATTEST_JUDGE_TIMEOUT_S` | Per-evaluation judge timeout in seconds. | `30` |
| `ATTEST_JUDGE_META_EVAL` | `true` runs each judge assertion three times and takes the median. | unset |
| `ATTEST_JUDGE_CACHE_MAX_MB` | Judge response cache size, clamped to 10–10000. | `100` |
| `ATTEST_RATE_LIMIT_OPENAI` | Judge requests per minute; must be > 0. | `60` |
| `ATTEST_RATE_LIMIT_OPENAI_BURST` | Judge request burst size; must be > 0. | `10` |

Point the judge at a local OpenAI-compatible server:

```bash
export ATTEST_JUDGE_BASE_URL=http://localhost:11434/v1
export ATTEST_JUDGE_API_KEY=unused
export ATTEST_JUDGE_MODEL=llama3.1
attest-engine doctor
```

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
