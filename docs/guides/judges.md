# Judges

Layer 6 (`llm_judge`) asks a model to score agent output against a rubric. The engine runs the judge itself, so the provider is chosen with environment variables and every SDK gets the same behavior. Deterministic layers (1–4, 7) never call a judge; prefer them, and use a judge for what only a model can check.

## Choose a provider

| `ATTEST_JUDGE_PROVIDER` | API | Key (first set wins) | `ATTEST_JUDGE_MODEL` | Default base URL |
|---|---|---|---|---|
| `openai` (default) | Chat Completions | `ATTEST_JUDGE_API_KEY`, `OPENAI_API_KEY` | optional, default `gpt-4.1` | `https://api.openai.com/v1` |
| `openai` + `ATTEST_JUDGE_BASE_URL` | Any OpenAI-compatible server (vLLM, Ollama `/v1`, LiteLLM) | `ATTEST_JUDGE_API_KEY` only | the server's model name | the URL you set |
| `anthropic` | Messages (`/v1/messages`) | `ATTEST_JUDGE_API_KEY`, `ANTHROPIC_API_KEY` | required | `https://api.anthropic.com` |
| `gemini` | `generateContent` (`/v1beta/models/<model>:generateContent`) | `ATTEST_JUDGE_API_KEY`, `GOOGLE_API_KEY`, `GEMINI_API_KEY` | required | `https://generativelanguage.googleapis.com` |
| `acp` (local only) | Agent Client Protocol over stdio, through `ATTEST_JUDGE_ACP_COMMAND` | none: the agent's own login | optional label | none |

Anthropic and Gemini have no built-in default model: a judge whose model silently changes is not the judge you calibrated, so `ATTEST_JUDGE_MODEL` is required and its absence is a startup error.

```bash
export ATTEST_JUDGE_PROVIDER=anthropic
export ANTHROPIC_API_KEY=...
export ATTEST_JUDGE_MODEL=<an Anthropic model id>
attest doctor        # shows provider, model, base URL and which variable supplied the key
```

With no key for the selected provider, the judge is not configured and every judge assertion returns `not_evaluated` with reason `provider_not_configured`.

### Keys and custom endpoints

`ATTEST_JUDGE_BASE_URL` points the selected provider at another host: a local OpenAI-compatible server, or an Anthropic/Gemini gateway that speaks the same API. For OpenAI-compatible servers the base URL includes `/v1` (`http://localhost:11434/v1`); for Anthropic and Gemini it is the root without a version segment (`https://gateway.example`), because the engine appends `/v1/messages` or `/v1beta/models/<model>:generateContent`. With a base URL set, only `ATTEST_JUDGE_API_KEY` is sent; a provider's standard key (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `GOOGLE_API_KEY`) never reaches a custom endpoint. Base URLs must be `http`/`https` without credentials, query or fragment. Plain `http` to a non-loopback host works but is flagged as insecure by the startup log and `doctor`. Keys travel in headers (`Authorization`, `x-api-key`, `x-goog-api-key`), never in URLs, and never appear in errors, logs or `doctor` output: a provider error that echoes the key has it replaced by `[redacted]`. Redirects are not followed.

Anthropic requests use the model's default sampling (current Claude models reject a non-default `temperature`); OpenAI and Gemini requests use temperature 0. Anthropic and Gemini requests carry an output budget of at least 4096 tokens, because their current models count reasoning against it and a budget sized for the JSON verdict alone would end before any text.

## Local judges on your Claude Code or Codex subscription (ACP)

`ATTEST_JUDGE_PROVIDER=acp` runs judges through a local [Agent Client Protocol](https://agentclientprotocol.com) agent, such as an ACP adapter for Claude Code or Codex, on the subscription you are already logged in with. The same agent also generates simulated user turns (`generate_user_message`, used by simulated users), because those calls go to the configured judge provider.

The agent starts with an explicit allowlist of environment variables, not a copy of yours, so unrelated credentials in variables (`GITHUB_TOKEN`, `AWS_SECRET_ACCESS_KEY`, API keys, CI tokens) never reach it and an exported key or endpoint switch cannot route it to API billing instead of your subscription. It receives `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `TMPDIR`, `TERM`, `LANG`, every `LC_*` except names that look like secrets (`LC_DEPLOY_TOKEN` is dropped), the login and configuration variables `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CONFIG_DIR` and `CODEX_HOME`, and the proxy and CA settings `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` (and their lowercase forms), `SSL_CERT_FILE` and `NODE_EXTRA_CA_CERTS`. On Windows it also receives `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `SystemRoot`, `ComSpec`, `PATHEXT`, `TEMP`, `TMP`, `HOMEDRIVE` and `HOMEPATH`; names are compared case-insensitively there. Everything else is dropped, including `OPENAI_*`, `ANTHROPIC_*`, `GEMINI_API_KEY`, `GOOGLE_API_KEY` and every `ATTEST_JUDGE_*` variable. If your agent needs another variable, wrap it in a launcher script that sets it.

Three credential channels still reach the agent, and an agent tool steered by the output under test could read them: proxy URLs are passed unchanged, including any `user:pass@` in them; the agent's own session variables (`CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME`) are passed by design; and `HOME` is passed, so credential files under it (`~/.aws`, `~/.npmrc`, `gh` configuration) stay readable. Use a proxy without embedded credentials and an agent configuration that denies tools (below) when the output under test is untrusted.

```bash
export ATTEST_JUDGE_PROVIDER=acp
export ATTEST_JUDGE_ACP_COMMAND="npx -y @zed-industries/claude-code-acp"   # or your agent's ACP command
export ATTEST_JUDGE_TIMEOUT_S=120    # agents start slower than an HTTP call
attest doctor
```

- **Never in CI.** A personal subscription is not a CI credential, so the engine refuses to start (and `doctor` exits 1) when any of `CI`, `GITHUB_ACTIONS`, `GITLAB_CI`, `BUILDKITE`, `CIRCLECI`, `JENKINS_URL`, `TF_BUILD` or `TEAMCITY_VERSION` is defined, even as an empty string, with a message naming the API-key providers to use instead.
- **Command.** `ATTEST_JUDGE_ACP_COMMAND` is split on whitespace and run directly, without a shell: no quoting, globbing or variable expansion. The executable must be on `PATH` or given as a path; a relative path is resolved against the directory the engine starts in. `ATTEST_JUDGE_BASE_URL` does not apply and is a startup error with `acp`. Do not put secrets in the command's arguments: logs and errors name only the executable's base name, but the full command is kept in memory and in the judge cache scope.
- **One agent per judgment.** Each judge call starts the agent in a new empty temporary directory, opens one session, sends the rubric and the agent output as one prompt, collects the agent's message text and stops the agent. Thought chunks and tool output are ignored, as are non-JSON lines a wrapper prints on stdout. Every call, judge or simulated user, is bounded by `ATTEST_JUDGE_TIMEOUT_S` (default 30) even when its caller sets no deadline. The agent runs in its own process group (process tree on Windows); when the call times out, the whole group is killed, so `npx` children do not outlive it.
- **The agent is not sandboxed.** The prompt contains output from the agent under test, which is untrusted and may try to instruct the judge. The engine advertises no file-system or terminal capability, refuses the agent's file-system and terminal requests, and rejects every permission request, so actions that go through the client are blocked. The agent still runs as you, with your network access, home directory and its own configuration: any tool your agent settings allow without asking (reads, shell commands, web fetches, MCP servers from its user config) can run on absolute paths, and the empty temporary working directory does not confine it. Run the judge with an agent configuration that denies tools or asks before every tool use. The engine logs this warning at startup.
- **Model.** ACP has no stable way to choose a model, so the agent uses its own configured model. Results record provider `acp` and the model label `ATTEST_JUDGE_MODEL`, or `acp:<command name>` when it is unset; set `ATTEST_JUDGE_MODEL` to the model your agent uses so calibration stays per model.
- **Limits and failures.** `ATTEST_RATE_LIMIT_ACP` and `ATTEST_RATE_LIMIT_ACP_BURST` bound how often agents start; each judge call, including bias probes and repeated samples, starts one. An agent that fails to start, errors, exits early, ends the turn with anything but `end_turn` or sends no text is reported as `not_evaluated: provider_unavailable` without a retry; only transient HTTP and network failures (408, 429, 5xx, connection errors) are retried.

## Rate limits and outages

Each provider has its own token-bucket limiter, read from `ATTEST_RATE_LIMIT_<PROVIDER>` (requests per minute, default 60) and `ATTEST_RATE_LIMIT_<PROVIDER>_BURST` (default 10): `ATTEST_RATE_LIMIT_OPENAI`, `ATTEST_RATE_LIMIT_ANTHROPIC`, `ATTEST_RATE_LIMIT_GEMINI`, `ATTEST_RATE_LIMIT_ACP`. Only transient failures are retried: HTTP 408, 429 and 5xx, and network errors. A retry waits an exponential backoff (500 ms doubling, capped at 30 s) with jitter, and waits at least as long as the provider's `Retry-After` header when it sends one; a call is retried up to three times. Any other failure is final. Every retry reserves a judge call against `ATTEST_MAX_JUDGE_CALLS` and `ATTEST_BUDGET_USD`.

A 401 or 403 is not retried: it costs exactly one call and the assertion is `not_evaluated` with reason `provider_auth`. Fix the key or `ATTEST_JUDGE_BASE_URL`; `attest doctor` shows what the engine would use.

When the provider is still unavailable after the retries, the assertion is `not_evaluated` with reason `provider_unavailable`. An outage, a rejected key or an unusable reply never becomes a pass or a failure: the test is skipped with the reason (`--attest-strict` turns it into a failure), and `attest policy evaluate` warns with exit 2 unless the policy sets `not_evaluated: block`. See [verdicts](verdicts.md).

## Judge replies are measurements

A reply counts only as a JSON object whose `score` is a number in [0,1], or an explicit abstain (below). Anything else is `not_evaluated` with reason `judge_malformed`: no JSON object, no `score` (it is not read as 0), a non-numeric score, or a score outside [0,1] (`{"score": 7}` is not a pass). A malformed reply is not cached and is not retried; it is a judge answer, not an outage. With `repeat: N`, a run with a malformed reply is left out of the median; when no run scored, the reason is the first that applies of `provider_auth`, `provider_unavailable`, `judge_malformed`, `judge_abstained`, `budget_exhausted`.

The agent output sits between delimiters that carry a random per-call nonce (`<<<AGENT_OUTPUT_START:<nonce>>>` and `<<<AGENT_OUTPUT_END:<nonce>>>`), so output that contains the plain delimiters cannot close the data block. A reply that echoes the nonce is `judge_malformed`. The nonce is replaced by the plain delimiters in the cache key and in record/replay matching, so replays and cache hits do not depend on it.

The spec is validated before the cache is consulted, and `repeat` and the bias probes are part of the cache key, so a cache hit never skips a spec error. An invalid spec is a `hard_fail` with failure class `spec_error`. For a judge below its threshold, `failure_class` compares the score to the assertion's own `threshold` (0.78 against 0.8 is `stochastic_variance`, not `broken_code`).

## Abstention

Built-in rubrics (version `v2`) tell the judge to answer `{"abstain": true, "reason": "<the evidence that is missing>"}` instead of guessing a score when the agent output lacks what the rubric needs. The engine reads only that structured field, never phrases in an explanation: the assertion is `not_evaluated` with reason `judge_abstained` and the judge's reason in its explanation, and the answer is not cached. With `repeat: N`, abstaining runs are left out of the median (the mean of the two middle scores for an even count); the assertion is `judge_abstained` only when no run scored. A bias probe whose judge abstains is skipped. Custom rubrics opt in by asking for the same object.

## Dynamic thresholds

`threshold` on `passes_judge` / `passesJudge` and `output_similar_to` / `outputSimilarTo` (Layer 5) is a number or `"dynamic"`. A missing threshold, or one at or below 0, is `0.8`. A judge threshold above 1 is a `spec_error`; any string other than `"dynamic"` is an invalid spec.

`"dynamic"` judges a score against the assertion's own history. The engine keeps one row per evaluation under the assertion id (ids are stable per test, see [baselines](baselines.md#removed-and-changed-assertions)), and reads the newest 50 rows that passed or soft-failed. Hard failures never enter that baseline, so a run of failures cannot lower the bar. With at least 10 such rows the pass bar is

```text
max(0.8, mean − 2·max(σ, 0.02))
```

and a score below it is a `hard_fail` (a `soft_fail` when the assertion is `soft`), even when the score is above 0.8. The explanation names the score, the bar, the baseline size, mean and σ, and the result carries `threshold_source: "dynamic"`. A `hard_fail` below the bar also carries `drift` (`assertion_id`, `mean`, `stddev`, `count` of baseline rows, `latest_score`, `deviation`, `status: "drift_detected"`). The floor of 0.8 means a dynamic assertion is never easier than the default static one; the σ floor of 0.02 keeps a history of identical scores from failing on noise. The window, the factor and both floors are fixed.

| History (newest score last) | Score | Result |
|---|---|---|
| ten runs at 0.9 | 0.5 | `hard_fail` with `drift` |
| ten runs at 1.0 | 0.99 | `pass` (bar 0.96) |
| ten runs at 0.0 | 0.0 | `hard_fail`: the zeros are not in the baseline, and 0.0 is below the static 0.8 |
| fewer than 10 passing or soft rows | any | the static threshold alone, no `drift` |

Evaluations that are `not_evaluated`, including simulation runs, are not recorded and never move a baseline. If the engine has no history store, or reading it fails, the assertion is judged by the static threshold, `threshold_source` is `dynamic_unavailable` and the explanation says so; a failing row then has failure class `missing_trace_data`.

History lives under `ATTEST_CACHE_DIR`. A CI job that starts with an empty cache is always in the cold start (fewer than 10 rows), so `"dynamic"` behaves as the static `0.8` there until the directory is restored between runs ([configuration](configuration.md#cache-and-history)). To get an alert instead of a failure, use the [continuous eval runner](continuous-eval.md).

## Budgets

`ATTEST_MAX_JUDGE_CALLS` caps judge requests per engine session and `ATTEST_BUDGET_USD` caps the spend of judge and embedding calls. Every call reserves one request and its worst-case cost before it is sent, so concurrent assertions, `repeat` runs, bias probes and retries cannot exceed the budget. The `acp` judge reports no token usage, so any USD limit, including a test's, refuses its calls; limit it by calls (a call that fails after it was sent keeps its reservation, and a request without an output limit is capped at 1024 tokens); a call that cannot reserve is not sent and its assertion is `not_evaluated: budget_exhausted`. Costs come from a built-in price table (`gpt-4.1`, `gpt-4.1-mini`, `gpt-4.1-nano`, `text-embedding-3-small`, `text-embedding-3-large`) or `ATTEST_JUDGE_PRICE_IN` / `ATTEST_JUDGE_PRICE_OUT` (USD per 1M tokens), and are reconciled to the token usage each provider reports. See [configuration](configuration.md#evaluation).

## What a result records

Every judge result carries `judge_metadata` with `provider` (`openai`, `anthropic`, `gemini`, `acp`) and `model`, alongside the rubric name and version, prompt hash and sample scores. Reports print them (`judge: anthropic/<model> / default @ v2`), so a score can always be traced to the model that produced it. Judge responses are cached per provider, endpoint (the ACP command for `acp`), rubric and model, so switching provider or model never reuses another judge's scores, even behind one gateway with the same model name.

## Calibration is per model

Agreement with human labels is a property of one judge model. `attest calibrate` stores labels keyed by judge provider and model as well as rubric, rubric version and prompt hash:

```bash
attest calibrate --labels labels.jsonl --rubric default \
  --persist --judge-provider anthropic --judge-model <model id>
```

`--persist` requires `--judge-provider` and `--judge-model`, naming the judge that produced the file's `judge_score` column. Labels for different providers or models are never pooled. Labels stored by an earlier version have no provider or model; the cache database is upgraded in place on first use, and those rows stay out of every provider's calibration.
