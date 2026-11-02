# Judges

Layer 6 (`llm_judge`) asks a model to score agent output against a rubric. The engine runs the judge itself, so the provider is chosen with environment variables and every SDK gets the same behavior. Deterministic layers (1–4, 7) never call a judge; prefer them, and use a judge for what only a model can check.

## Choose a provider

| `ATTEST_JUDGE_PROVIDER` | API | Key (first set wins) | `ATTEST_JUDGE_MODEL` | Default base URL |
|---|---|---|---|---|
| `openai` (default) | Chat Completions | `ATTEST_JUDGE_API_KEY`, `OPENAI_API_KEY` | optional, default `gpt-4.1` | `https://api.openai.com/v1` |
| `openai` + `ATTEST_JUDGE_BASE_URL` | Any OpenAI-compatible server (vLLM, Ollama `/v1`, LiteLLM) | `ATTEST_JUDGE_API_KEY` only | the server's model name | the URL you set |
| `anthropic` | Messages (`/v1/messages`) | `ATTEST_JUDGE_API_KEY`, `ANTHROPIC_API_KEY` | required | `https://api.anthropic.com` |
| `gemini` | `generateContent` (`/v1beta/models/<model>:generateContent`) | `ATTEST_JUDGE_API_KEY`, `GOOGLE_API_KEY`, `GEMINI_API_KEY` | required | `https://generativelanguage.googleapis.com` |

Anthropic and Gemini have no built-in default model: a judge whose model silently changes is not the judge you calibrated, so `ATTEST_JUDGE_MODEL` is required and its absence is a startup error.

```bash
export ATTEST_JUDGE_PROVIDER=anthropic
export ANTHROPIC_API_KEY=...
export ATTEST_JUDGE_MODEL=<an Anthropic model id>
attest doctor        # shows provider, model, base URL and which variable supplied the key
```

With no key for the selected provider, the judge is not configured and every judge assertion returns `not_evaluated` with reason `provider_not_configured`.

### Keys and custom endpoints

`ATTEST_JUDGE_BASE_URL` points the selected provider at another host: a local OpenAI-compatible server, or an Anthropic/Gemini gateway that speaks the same API. For OpenAI-compatible servers the base URL includes `/v1` (`http://localhost:11434/v1`); for Anthropic and Gemini it is the root without a version segment (`https://gateway.example`), because the engine appends `/v1/messages` or `/v1beta/models/<model>:generateContent`. With a base URL set, only `ATTEST_JUDGE_API_KEY` is sent; a provider's standard key (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `GOOGLE_API_KEY`) never reaches a custom endpoint. Base URLs must be `http`/`https` without credentials, query or fragment. Plain `http` to a non-loopback host works but is flagged as insecure by the startup log and `doctor`. Keys travel in headers (`Authorization`, `x-api-key`, `x-goog-api-key`), never in URLs, and never appear in errors, logs or `doctor` output.

Anthropic requests use the model's default sampling (current Claude models reject a non-default `temperature`); OpenAI and Gemini requests use temperature 0. Anthropic and Gemini requests carry an output budget of at least 4096 tokens, because their current models count reasoning against it and a budget sized for the JSON verdict alone would end before any text.

## Rate limits and outages

Each provider has its own token-bucket limiter, read from `ATTEST_RATE_LIMIT_<PROVIDER>` (requests per minute, default 60) and `ATTEST_RATE_LIMIT_<PROVIDER>_BURST` (default 10): `ATTEST_RATE_LIMIT_OPENAI`, `ATTEST_RATE_LIMIT_ANTHROPIC`, `ATTEST_RATE_LIMIT_GEMINI`. A failed call, including HTTP 429 and 5xx, is retried up to three times with exponential backoff.

When the provider is still unavailable after the retries, or its reply cannot be parsed as a score, the assertion is `not_evaluated` with reason `provider_unavailable`. An outage never becomes a pass or a failure: the test is skipped with the reason (`--attest-strict` turns it into a failure), and `attest policy evaluate` warns with exit 2 unless the policy sets `not_evaluated: block`. See [verdicts](verdicts.md).

## What a result records

Every judge result carries `judge_metadata` with `provider` (`openai`, `anthropic`, `gemini`) and `model`, alongside the rubric name and version, prompt hash and sample scores. Reports print them (`judge: anthropic/<model> / default @ v1`), so a score can always be traced to the model that produced it. Judge responses are cached per endpoint, rubric and model, so switching provider or model never reuses another judge's scores.

## Calibration is per model

Agreement with human labels is a property of one judge model. `attest calibrate` stores labels keyed by judge provider and model as well as rubric, rubric version and prompt hash:

```bash
attest calibrate --labels labels.jsonl --rubric default \
  --persist --judge-provider anthropic --judge-model <model id>
```

`--persist` requires `--judge-provider` and `--judge-model`, naming the judge that produced the file's `judge_score` column. Labels for different providers or models are never pooled. Labels stored by an earlier version have no provider or model; the cache database is upgraded in place on first use, and those rows stay out of every provider's calibration.
