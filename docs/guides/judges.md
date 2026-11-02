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

The engine removes these variables from the agent's environment, so an exported key or endpoint switch does not route the agent to API billing instead of your subscription: every `ATTEST_JUDGE_*` variable, `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `CODEX_API_KEY`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`, `GEMINI_API_KEY` and `GOOGLE_API_KEY` (names compared case-insensitively on Windows). The rest of your environment (`HOME`, `PATH`, the agent's own settings) is passed through so the agent finds its login.

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
- **Limits and failures.** `ATTEST_RATE_LIMIT_ACP` and `ATTEST_RATE_LIMIT_ACP_BURST` bound how often agents start; each judge call, including bias probes and repeated samples, starts one. An agent that fails to start, errors, exits early, ends the turn with anything but `end_turn` or sends no text is retried, then reported as `not_evaluated: provider_unavailable`.

## Rate limits and outages

Each provider has its own token-bucket limiter, read from `ATTEST_RATE_LIMIT_<PROVIDER>` (requests per minute, default 60) and `ATTEST_RATE_LIMIT_<PROVIDER>_BURST` (default 10): `ATTEST_RATE_LIMIT_OPENAI`, `ATTEST_RATE_LIMIT_ANTHROPIC`, `ATTEST_RATE_LIMIT_GEMINI`, `ATTEST_RATE_LIMIT_ACP`. A failed call, including HTTP 429 and 5xx, is retried up to three times with exponential backoff.

When the provider is still unavailable after the retries, or its reply cannot be parsed as a score, the assertion is `not_evaluated` with reason `provider_unavailable`. An outage never becomes a pass or a failure: the test is skipped with the reason (`--attest-strict` turns it into a failure), and `attest policy evaluate` warns with exit 2 unless the policy sets `not_evaluated: block`. See [verdicts](verdicts.md).

## What a result records

Every judge result carries `judge_metadata` with `provider` (`openai`, `anthropic`, `gemini`, `acp`) and `model`, alongside the rubric name and version, prompt hash and sample scores. Reports print them (`judge: anthropic/<model> / default @ v1`), so a score can always be traced to the model that produced it. Judge responses are cached per provider, endpoint (the ACP command for `acp`), rubric and model, so switching provider or model never reuses another judge's scores, even behind one gateway with the same model name.

## Calibration is per model

Agreement with human labels is a property of one judge model. `attest calibrate` stores labels keyed by judge provider and model as well as rubric, rubric version and prompt hash:

```bash
attest calibrate --labels labels.jsonl --rubric default \
  --persist --judge-provider anthropic --judge-model <model id>
```

`--persist` requires `--judge-provider` and `--judge-model`, naming the judge that produced the file's `judge_score` column. Labels for different providers or models are never pooled. Labels stored by an earlier version have no provider or model; the cache database is upgraded in place on first use, and those rows stay out of every provider's calibration.
