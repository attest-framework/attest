# Engine configuration

The engine reads its configuration from environment variables when it starts. An invalid provider, URL or integer setting stops the engine with an error naming the variable. Since 1.1.0 that includes `ATTEST_EVAL_CONCURRENCY`, `ATTEST_EMBEDDING_CACHE_MAX_MB`, `ATTEST_JUDGE_CACHE_MAX_MB`, `ATTEST_HISTORY_MAX_ROWS`, `ATTEST_HISTORY_MAX_AGE_DAYS` and `ATTEST_JUDGE_TIMEOUT_S`: a value that is not a positive integer makes the engine and `attest-engine doctor` exit 1, naming every offending variable. `ATTEST_JUDGE_META_EVAL` keeps its historical behavior: it is enabled only by the exact value `true`.

## Engine discovery and the `attest` command

Each SDK release requires one engine version, `ENGINE_VERSION`, equal to the SDK's own version (for example `1.0.0`). The SDK looks for an engine in this order and uses the first one it finds:

1. `--attest-engine` (pytest) or the `engine_path` / `enginePath` argument of `EngineManager`
2. `ATTEST_ENGINE_PATH`
3. `attest-engine` (`attest-engine.exe` on Windows) in a `PATH` directory; empty entries are skipped, so the current directory is never searched
4. `~/.attest/bin/<ENGINE_VERSION>/attest-engine`
5. A download of `ENGINE_VERSION` from GitHub Releases into that directory, verified against the hash shipped inside the SDK package (see [How the download is verified](#how-the-download-is-verified)) and written atomically. `ATTEST_ENGINE_NO_DOWNLOAD=1` turns the download off.

The engine found must report exactly `ENGINE_VERSION` (`attest-engine version`). Otherwise the SDK stops with a version-mismatch error naming the engine, where it came from, the version it reports and the version required. It never skips a mismatched engine in favor of the next location. The pytest fixture, Vitest, `EngineManager` and the `attest` command all use this check. The legacy unversioned `~/.attest/bin/attest-engine` from releases before 1.0 is neither used nor deleted.

### How the download is verified

Each published `attest-ai` wheel and sdist and the `@attest-ai/core` tarball carries `engine-checksums.json`, a table of `version → platform → sha256` generated in the same release run from the engine binaries that run built. The downloader trusts only that table. The release's own `checksums-sha256.txt` is never consulted, so a replaced release asset cannot vouch for itself.

- A binary whose hash differs from the shipped one is refused and nothing is written. The error reads `does not match the hash shipped in attest-ai <version>` (Python) or `does not match the hash shipped in @attest-ai/core <version>` (TypeScript).
- Downloads use https only. A redirect to any other scheme is refused, a relative `Location` resolves against the URL that returned it, and at most 5 redirects are followed.
- The connect phase times out after 10 seconds and the whole download, redirects included, after 120 seconds.
- A source checkout or editable install has no table and never downloads. It fails naming `ATTEST_ENGINE_PATH`; build an engine with `make engine` and point `ATTEST_ENGINE_PATH` at `./bin/attest-engine`.
- `ATTEST_ENGINE_PATH` and the exact-version pin are unchanged.
- When the download fails, for example offline or behind a proxy that blocks it, the pytest plugin ends the run with one error line that names `ATTEST_ENGINE_PATH`, without a traceback. The download notice itself is printed with pytest's output capture off, so it shows even without `-s`. The Vitest global setup resolves the engine once before the tests, so an engine that cannot be found fails the run before the first test.

Every release binary also carries a GitHub build-provenance attestation, created by the `attest-engine` job of the release workflow. To check a binary you downloaded yourself, pin the workflow and the tag, not just the repository:

<!-- snippet: skip reason="verifies a release asset with the GitHub CLI (network)" -->
```bash
gh attestation verify attest-engine-linux-amd64 \
  --repo attest-framework/attest \
  --signer-workflow attest-framework/attest/.github/workflows/release.yml \
  --source-ref refs/tags/v<VERSION>
```

Passing means the file's SHA-256 matches an attestation signed by `release.yml` in `attest-framework/attest` on the `v<VERSION>` tag. Only repository admins can create `v*` tags, and the release workflow refuses a tag whose commit is not on `main`; to check that yourself, confirm the attested commit (`gh attestation verify … --format json`) is reachable from `main`. The attestation is created after the release is published, so a binary from a release that was never published has none. It does not tell you the source at that tag is free of bugs, only where and how the binary was built. `--repo` alone accepts an attestation from any workflow in the repository on any ref, so it is not enough on its own. The SDKs do not run this check; they compare against the table shipped in the package.

The `attest` command (`uv run attest`, `npx attest`) implements `attest init` itself and forwards every other command to the version-checked engine, passing arguments, output and exit code through unchanged:

<!-- snippet: fragment reason="command overview; report.json comes from your own run" -->
```bash
attest --help                     # SDK commands, plus engine commands when one is installed
attest --version                  # SDK version and the engine version it requires
attest init                       # scaffold a test project (no engine needed)
attest doctor --json              # = attest-engine doctor --json
attest policy evaluate --policy attest.policy.yaml --report report.json
```

Before forwarding, the CLI checks that the engine's `help` lists the command. If no matching engine is available, or the engine predates the command, the command exits 1 before anything runs, so `attest policy evaluate` never exits 0 without evaluating. A command killed by a signal exits 128 + N. `attest --help` never downloads: it lists the engine's commands only when a matching engine is already available, and always exits 0. Run tests with `pytest` or `vitest` directly.

## Check your setup

`attest doctor` (or `attest-engine doctor`) reports what the engine would start with under the current environment: engine and protocol versions, the capabilities `initialize` advertises (`judge_configured` and `embedding_configured` say a provider exists; `llm_judge` and `embedding` only say the assertion type is accepted), the judge and embedding providers, simulation mode and the cache directory.

```bash
attest doctor          # human-readable
attest doctor --json   # machine-readable, schema: proto/attest/v1/doctor.schema.json
```

The output never contains an API key. It shows only which variable the judge key came from. A configuration error exits 1 with the same message the server would fail to start with. In `onnx` builds that select ONNX embeddings, `doctor` constructs the embedder just as the server does, so the first run downloads the model.

`attest-engine --help` lists every command. An unknown command exits 2.

## LLM judge (Layer 6)

The judge runs natively on OpenAI, Anthropic and Gemini, on any OpenAI-compatible endpoint (vLLM, Ollama's `/v1` API, LiteLLM, Azure-compatible gateways), and locally through an ACP agent on your Claude Code or Codex subscription. Provider setup, rate limits and calibration per model: [Judges](judges.md).

| Variable | Purpose | Default |
|---|---|---|
| `ATTEST_JUDGE_PROVIDER` | `openai` (also OpenAI-compatible endpoints), `anthropic`, `gemini`, or `acp` (local only). Any other value is a startup error. | `openai` |
| `ATTEST_JUDGE_ACP_COMMAND` | With `acp`: the command that starts the ACP agent, split on whitespace and run without a shell. The agent starts with an allowlisted environment; see [judges](judges.md#local-judges-on-your-claude-code-or-codex-subscription-acp). | unset |
| `CI` (also `GITHUB_ACTIONS`, `GITLAB_CI`, `BUILDKITE`, `CIRCLECI`, `JENKINS_URL`, `TF_BUILD`, `TEAMCITY_VERSION`) | When any is defined, even empty, `ATTEST_JUDGE_PROVIDER=acp` is a startup error. | unset |
| `ATTEST_JUDGE_API_KEY` | Judge API key. Takes precedence over the provider's standard key variable, and is required with `ATTEST_JUDGE_BASE_URL`. For an endpoint without authentication, set any non-empty value. | unset |
| `OPENAI_API_KEY` | OpenAI's standard key variable. Used for the `openai` judge when `ATTEST_JUDGE_API_KEY` is unset and no `ATTEST_JUDGE_BASE_URL` is set, and for OpenAI embeddings. Never sent to a custom base URL. | unset |
| `ANTHROPIC_API_KEY` | Anthropic's standard key variable, used for the `anthropic` judge under the same rules. | unset |
| `GOOGLE_API_KEY`, `GEMINI_API_KEY` | Gemini's standard key variables (in that order, as in Google's SDK), used for the `gemini` judge under the same rules. | unset |
| `ATTEST_JUDGE_BASE_URL` | Base URL of the selected provider's API, e.g. `http://localhost:11434/v1` for an OpenAI-compatible server (include `/v1`), or the root of an Anthropic or Gemini gateway without a version segment (the engine appends `/v1/messages` or `/v1beta/models/...`). Requires `ATTEST_JUDGE_API_KEY`. Must be `http`/`https` without credentials, query or fragment; use `https` for anything other than a local endpoint, because the key is sent with every request. | `https://api.openai.com/v1`, `https://api.anthropic.com`, `https://generativelanguage.googleapis.com` |
| `ATTEST_JUDGE_MODEL` | Judge model name. Required for `anthropic` and `gemini`; for `acp` a label recorded on results. | `gpt-4.1` for `openai`, `acp:<command name>` for `acp` |
| `ATTEST_JUDGE_TIMEOUT_S` | Per-evaluation judge timeout in seconds; must be a positive integer. | `30` |
| `ATTEST_JUDGE_META_EVAL` | `true` runs each judge assertion three times and takes the median. | unset |
| `ATTEST_JUDGE_CACHE_MAX_MB` | Judge response cache size, clamped to 10–10000. | `100` |
| `ATTEST_RATE_LIMIT_<PROVIDER>` | Judge requests per minute for the selected provider (`ATTEST_RATE_LIMIT_OPENAI`, `ATTEST_RATE_LIMIT_ANTHROPIC`, `ATTEST_RATE_LIMIT_GEMINI`, `ATTEST_RATE_LIMIT_ACP`); must be > 0. A judge call that fails with HTTP 408, 429, 5xx or a network error is retried up to three times with jittered backoff and the provider's `Retry-After`; a 401 or 403 is not retried (see [judges](judges.md#rate-limits-and-outages)). | `60` |
| `ATTEST_RATE_LIMIT_<PROVIDER>_BURST` | Judge request burst size for the selected provider; must be > 0. | `10` |

Point the judge at a local OpenAI-compatible server:

<!-- snippet: skip reason="points the judge at a local Ollama server" -->
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

A `similar_to` / `output_similar_to` assertion with `threshold="dynamic"` compares each score with the history of that assertion; see [dynamic thresholds](judges.md#dynamic-thresholds).

## Evaluation

| Variable | Purpose | Default |
|---|---|---|
| `ATTEST_SIMULATION` | `1`/`true`/`yes`: Layers 5–6 return `not_evaluated(simulation)` and no provider is called. | unset |
| `ATTEST_MAX_SOFT_FAILURES` | Maximum soft failures allowed in one engine session, counted across batches; `0` allows none. Exceeding it fails the batch. | unset (no limit) |
| `ATTEST_EVAL_CONCURRENCY` | Layer 5–6 worker pool size per batch; must be a positive integer. | `8` |
| `ATTEST_MAX_JUDGE_CALLS` | Maximum judge-provider requests in one engine session (judges, `repeat` runs, bias probes, simulated-user messages, retries). Each request reserves one call before it is sent; a request that cannot reserve is not sent and its assertion is `not_evaluated: budget_exhausted`. ACP judge calls count. | unset (no limit) |
| `ATTEST_BUDGET_USD` | Maximum USD for attest-initiated model calls (layer 5 OpenAI embeddings and every judge request) in one engine session. Each call reserves its worst-case cost (price × prompt bytes + price × output-token limit) before it is sent and is reconciled to the reported token usage when it returns; a call that fails after it was sent keeps its reservation, since the provider may have billed it. A default judge model or embedding model without a price is a startup error, and so is the `acp` judge, which reports no token usage; an assertion that names an unpriced `model` is refused at run time (`budget_exhausted`). | unset (no limit) |
| `ATTEST_JUDGE_PRICE_IN`, `ATTEST_JUDGE_PRICE_OUT` | Judge price in USD per 1M input and output tokens, set together. Overrides the built-in table (`gpt-4.1`, `gpt-4.1-mini`, `gpt-4.1-nano`) for every judge call; required for `ATTEST_BUDGET_USD` with any other judge model. | unset |
| `ATTEST_ENGINE_TIMEOUT` | How long the SDK waits for the engine's reply to one request. Read by the SDK, not the engine. Seconds in both Python and TypeScript (TypeScript read milliseconds before 1.1.0; a value above 3600 is rejected with a message naming the unit). On a timeout the SDK restarts the engine and fails every pending request, so a late reply never reaches another test. The new engine gets the session budget that is left. Each request lost in the restart that can call a model (a batch with a judge or embedding assertion, or a simulated-user message) holds its `budget` limits if it sent them, otherwise everything that is left; deterministic requests hold nothing. Spend the SDK could not observe earlier (simulated-user messages, engine errors) leaves no budget after a restart. So a timeout can exhaust `ATTEST_MAX_JUDGE_CALLS` / `ATTEST_BUDGET_USD` for the rest of the session. | `30` (seconds) |

Under pytest-xdist each worker is a separate pytest process with its own engine session, so `ATTEST_MAX_JUDGE_CALLS`, `ATTEST_BUDGET_USD` and `ATTEST_MAX_SOFT_FAILURES` apply per worker, not across the run. Split the budget by the worker count: with `-n 4` and a total of 40 judge calls, set `ATTEST_MAX_JUDGE_CALLS=10`.

## Cache and history

| Variable | Purpose | Default |
|---|---|---|
| `ATTEST_REPORT_TRACES` | `1` embeds the evaluated traces in the report-v2 file (`traces` and a `trace_id` per row) for `attest view`. Traces hold prompts, outputs and tool arguments. Read by the pytest plugin and the Vitest reporter. See [viewer](viewer.md). | unset |
| `ATTEST_CONTRACTS_DIR` | Directory where each engine process records the assertion specs it evaluates (one `0600` NDJSON file per process, no traces or outputs), for `attest mutate --contracts`. Off when unset. See [mutation](mutation.md). | unset |
| `ATTEST_CACHE_DIR` | Cache, baseline and history database directory. | `~/.attest/cache` |
| `ATTEST_HISTORY_MAX_ROWS` | Result history row limit (enables pruning). Pruning runs in the background in short batches, so a write never waits for it to finish. | `10000` when pruning is enabled |
| `ATTEST_HISTORY_MAX_AGE_DAYS` | Result history age limit in days (enables pruning). | `30` when pruning is enabled |

The history is what `threshold="dynamic"` and the [continuous eval runner](continuous-eval.md) read: one row per evaluated assertion id, with `not_evaluated` rows left out. Rows recorded before 1.1 are keyed by ordinal ids and match no assertion, so each assertion rebuilds its baseline (10 passing or soft runs) after the upgrade.

## Egress guard and proxies

| Variable | Purpose |
|---|---|
| `ATTEST_EGRESS` | `block` fails connections from the test process to known model-provider hosts; `allow` and unset leave them alone; any other value is an error. Equivalent to `pytest --attest-egress=block` and `useReplay(name, { egress: "block" })`. |
| `HTTPS_PROXY`, `HTTP_PROXY`, `ALL_PROXY` (either case) | With the guard on, requests a client sends to the named HTTP proxy are screened: a `CONNECT` target or absolute-form request that names a provider host is blocked. `NO_PROXY` is not read. |

The guard is a best-effort check for trusted test code, not an isolation boundary: child processes, SOCKS proxies and `https://` proxy connections are not inspected, and after an allowed `CONNECT` it does not check what the tunnel carries. The provider host list and the full limits are in [replay](replay.md#block-direct-provider-connections).

## Removed variables

These variables are no longer read. Setting them has no effect.

| Removed | Use instead |
|---|---|
| `ATTEST_OPENAI_API_KEY` | `ATTEST_JUDGE_API_KEY` for the judge, `OPENAI_API_KEY` for the judge and OpenAI embeddings |
| `ATTEST_BUDGET_MAX_COST` | `ATTEST_MAX_SOFT_FAILURES` (it always counted soft failures, not cost) |
| `ATTEST_JUDGE_RPM`, `ATTEST_JUDGE_BURST` | `ATTEST_RATE_LIMIT_OPENAI`, `ATTEST_RATE_LIMIT_OPENAI_BURST` |
