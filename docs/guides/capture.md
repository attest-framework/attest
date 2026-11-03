# Capture: turn a run into a regression fixture

`attest capture` runs your agent once, records what it did (its trace and every model call it made), removes secrets, and writes a fixture: `tests/attest/fixtures/<name>.attest.json`. It drafts deterministic contracts from the run, checks them against it, and writes a test file that runs those contracts. Capture an incident and you get a test that reproduces it; capture a good run and you get a test that keeps it good.

The command runs in `attest-engine`; `uv run attest capture …` and `npx attest capture …` forward to the engine of the installed SDK version and then draft the test file in that SDK's language.

## Capture a command

```bash
uv run attest capture --name inc-1042 --expect-fail -- make incident
npx attest capture --name inc-1042 --expect-fail -- npm run incident
```

```text
capture inc-1042: tests/attest/fixtures/inc-1042.attest.json
  trace: 5 llm_call, 4 tool_call; trace check OK
  exchanges: 5 recorded (openai chat_completions ×5)
  contracts:
    pass       tools-called                               trajectory (superset) matches [lookup_order issue_refund notify_customer]
    pass       order-lookup_order-before-issue_refund     lookup_order (step 1) precedes issue_refund (step 3)
    pass       order-issue_refund-before-notify_customer  issue_refund (step 3) precedes notify_customer (step 7)
    pass       cardinality-lookup_order-per-order_id      lookup_order within max 1 for each of 1 order_id value(s)
    hard_fail  cardinality-issue_refund-per-order_id      issue_refund called 2× for order_id=O-1042 (max 1)
    pass       cardinality-notify_customer-per-order_id   notify_customer within max 1 for each of 1 order_id value(s)
  verified: incident reproduction; expected failure: cardinality-issue_refund-per-order_id
  test: drafted tests/attest/test_inc_1042.py
```

The engine opens one listener on `127.0.0.1` (an ephemeral port) and runs the command with three environment variables pointing at it:

| Variable | Points at | Read by |
| --- | --- | --- |
| `OPENAI_BASE_URL` | the recording proxy, `/<token>/openai/v1` | OpenAI SDKs (Chat Completions and Responses) |
| `ANTHROPIC_BASE_URL` | the recording proxy, `/<token>/anthropic` | Anthropic SDKs (Messages) |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | the trace receiver, `/<token>/v1/traces` | OpenTelemetry OTLP/HTTP exporters |

`<token>` is 128 random bits chosen for each capture; requests without it get 404 and are not recorded.

The proxy forwards each model call to the upstream named by the same variable in the environment you ran `attest capture` from (default `https://api.openai.com/v1` and `https://api.anthropic.com`), so a capture against a local or scripted model server works by setting `OPENAI_BASE_URL` before the command. Streaming and non-streaming calls are both recorded; streamed events reach your agent as the upstream sends them. Your API key passes through to the upstream and is never written down.

The listener closes when the command exits. A command that exits non-zero still produces a fixture; its exit code is recorded. `SIGINT` and `SIGTERM` sent to `attest capture` are passed to the command. A model call the agent cancels or does not read to the end, and a trace export the receiver rejected or that is still in flight when the listener closes (up to 5 s after the command exits), fail the capture with no fixture written, since the fixture could not replay the run.

### What the agent must do

- Construct its model client from the standard variables. The official OpenAI and Anthropic SDKs read `OPENAI_BASE_URL` and `ANTHROPIC_BASE_URL` themselves; a client built with a hard-coded `base_url` bypasses the proxy, and the summary says `no model calls reached the proxy`.
- Export spans over OTLP/HTTP when `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is set: in Python `opentelemetry-exporter-otlp-proto-http` (`OTLPSpanExporter()` reads the variable), in Node `@opentelemetry/exporter-trace-otlp-http`. The receiver accepts `http/protobuf` and `http/json`, gzip or not.
- Flush the tracer provider before exiting (`provider.shutdown()` or `force_flush()`), so batched spans arrive. With no spans the capture fails: `no spans were received`.

Spans follow the OpenTelemetry GenAI conventions that [`attest trace check`](trace-check.md) reads; the captured spans must pass it, or the fixture is not verified.

## Incident reproductions and verified runs

Capture drafts these contracts from the run:

| Contract | Drafted as |
| --- | --- |
| `tools-called` | every tool the run called is called (`trajectory(..., mode="superset")`) |
| `order-<a>-before-<b>` | each tool's first call keeps its order (`tool_called_before`) |
| `cardinality-<tool>-per-<arg>` | each tool called with an `id` or `*_id` argument is called at most N times per value of it (`tool_cardinality(tool, per=arg, at_most=N)`; N below) |
| `output-schema` | the structured output keeps its keys and their JSON types (`output_matches_schema`) |
| `cost-ceiling` | the run costs at most 1.5× the observed `metadata.cost_usd` (`cost_under`) |

Cardinality depends on the capture mode. A normal capture sets N to the most calls the run made with any one value, so a healthy agent that reads an order twice still verifies. With `--expect-fail`, N is 1 for every such tool, so the calls the incident repeated fail: capturing INC-1042 that way fails only `cardinality-issue_refund-per-order_id` (it refunded order O-1042 twice) and records it as the expected failure. A normal capture of the same run drafts `issue_refund` at 2 and verifies, since without `--expect-fail` capture cannot know the run is an incident.

- Without `--expect-fail`, the fixture is **verified** when every drafted contract passes and the captured trace passes trace check; otherwise it is unverified and `attest capture` exits 1, naming the reason.
- With `--expect-fail`, the fixture is an **incident reproduction**: verified when at least one drafted contract fails and the rest pass. The failing contracts are recorded as `expected_failures`.

## The drafted test

For a verified fixture, `attest capture` writes `tests/attest/test_<name>.py` (Python; the name lowercased, other characters as `_`) or `tests/attest/<name>.test.ts` (TypeScript) next to the fixture directory, unless that file already exists: re-capturing never overwrites a test you edited. An unverified fixture gets no test, so capturing again with `--expect-fail` (or after editing the agent) still drafts one. The test loads the fixture and runs the contracts against its trace without running the agent:

```python
FIXTURE = load_fixture(Path(__file__).parent / "fixtures/inc-1042.attest.json")


def contracts() -> ExpectChain:
    return (
        expect(FIXTURE.trace)
        # cardinality-issue_refund-per-order_id: issue_refund at most once per order_id
        .tool_cardinality("issue_refund", per="order_id", at_most=1)
        # ...
    )


def test_inc_1042_reproduces_incident(attest: AttestEngineFixture) -> None:
    result = attest.inspect(contracts())

    statuses = [row.status for row in result.assertion_results]
    assert statuses == [status for _, status in EXPECTED]
```

For an incident reproduction the test uses `inspect()` and asserts each contract's recorded status, so it stays green while it proves the contract catches the incident. For a verified run it uses `evaluate()` and enforces every contract. The TypeScript file does the same with `attestExpect`, `loadFixture` and `inspect` / `evaluate` from `@attest-ai/vitest`.

## Refresh a fixture

`attest capture --refresh inc-1042` prints the command recorded in the fixture (`source.command`), asks for confirmation (`y` at a terminal; without one, `--yes`), then runs it, with the flags it was captured with (`--expect-fail` included), and overwrites the fixture. **Review the printed command before confirming a fixture you did not record:** the fixture is a file in your repository, and whoever can change it can change the command that `--refresh` runs. Run it from the directory the original capture ran in, with the environment the command needs. It refuses with an explanation when the command's arguments were redacted (a secret passed as an argument), and for fixtures captured with `--from` or in process, which have no command; re-run those captures yourself. A replay miss names this command (see `attest-engine replay`).

## Capture in process

When the agent runs inside your own process (a notebook, a script, a test), capture a block of code:

```python
import attest

with attest.record("inc-1042", expect_fail=True) as capture:
    client = OpenAI()           # construct clients inside the block
    run_refund_agent(client, "O-1042")

print(capture.result.fixture, capture.result.verified)
```

```ts
import { record } from "@attest-ai/core";

const result = await record(
  "inc-1042",
  async () => {
    const client = new OpenAI(); // construct clients inside the callback
    await runRefundAgent(client, "O-1042");
  },
  { expectFail: true, flush: () => provider.forceFlush() },
);
```

`record()` starts `attest-engine capture --serve`, sets the three variables in the process environment for the block, restores them afterwards, and writes the fixture and the drafted test. Model clients and OTLP exporters read the variables when they are constructed, so build them inside the block. Python flushes the global OpenTelemetry tracer provider on exit when the application loaded one; in TypeScript pass the provider's `forceFlush` as `flush`. Child processes started inside the block inherit the variables; `capture.env` holds them for a subprocess started with an explicit environment.

If the block raises, the capture is abandoned and no fixture is written. A fixture that is not verified raises `attest.CaptureError` (TypeScript: `CaptureError`) after it is written, and no test is drafted.

Options: `out_dir` / `outDir` (default `tests/attest/fixtures`), `expect_fail` / `expectFail`, `agent_id` / `agentId`, `emit_test` / `emitTest` (default true), `engine_path` / `enginePath`, and in TypeScript `flush`. `record()` sets process-wide variables, so captures cannot overlap; a second `record()` while one runs raises `CaptureError`.

## Capture from an export

```bash
uv run attest capture --name inc-1042 --expect-fail --from incidents/INC-1042/trace.py.otlp.json
```

`--from` builds the fixture from an OTLP/JSON export. It has the trace and the drafted contracts but no model exchanges, so it is marked `"replay_capable": false`.

## The fixture

A fixture follows `proto/attest/v1/fixture-v1.schema.json`:

| Field | Holds |
| --- | --- |
| `source` | `kind` (`command`, `in_process`, `otlp_file`), `replay_capable`, and for a command its arguments and exit code |
| `trace` | the run as an Attest trace (schema version 2), as `attest trace convert` writes it |
| `exchanges` | every model call in request order: provider, API (`chat_completions`, `responses`, `messages`, `other`), path, request body, response status, content type and body (the JSON value, or the event-stream text for a streamed call), and its `key` |
| `contracts` | the drafted contracts as engine assertion specs |
| `verification` | `verified`, `expect_fail`, the capture-time trace check, each contract's status and explanation, and `expected_failures` |
| `world` | reserved for simulated-world state; `null` |

Each exchange's `key` identifies the request: the `model`, the request parameters other than the prompt and the tools (`params`), and SHA-256 hashes of the prompt (`messages`; `input` and `instructions`; `system` and `messages`) and of the tool schemas. Fixtures record the exchanges and their keys; replaying them is not part of this release.

### Secrets

Request headers, which carry the API key, are never recorded. Everywhere else in the fixture, including object keys, capture replaces with `[redacted]`:

- the value of every environment variable whose name ends in `KEY`, `TOKEN`, `SECRET`, `PASSWORD`, `_PASS`, `_PWD`, `PASSWD`, `CREDENTIALS`, `CREDENTIAL`, `DSN` or `AUTH` (values of eight characters or more; `PWD`, `OLDPWD` and `*_ASKPASS` are paths, not secrets), and the password of any environment value that is a URL with userinfo. For `record()` this includes variables the block sets itself, such as keys loaded from a dotenv file;
- strings shaped like known credentials: `sk-…` (OpenAI, Anthropic), `sk_live_` / `sk_test_` / `rk_live_` (Stripe), `gsk_` (Groq), `hf_` (Hugging Face), `xai-`, `AKIA…`, `AIza…`, `ghp_` / `gho_` / `ghs_` / `github_pat_`, `xox…-`, `Bearer …`, PEM private keys, and the password in any `scheme://user:password@`;
- the command line is recorded in the fixture. The values of flags whose name contains `key`, `token`, `secret`, `password`, `pass`, `auth` or `credential` (`--api-key=…`, `--api-key …`, `-token …`) and of `NAME=value` arguments whose name is a secret by the environment name rule above, whatever the value's length (`env DB_PASSWORD=… make incident`) are redacted; one-letter flags (`-p …`) are recorded as they are, so pass secrets in the environment, not on the command line. The values of `key`, `api_key`, `apikey`, `token`, `access_token`, `sig` and `signature` query parameters in exchange paths are redacted too; paths are stored percent-decoded;
- secrets a streamed response spells out across several deltas (Chat Completions content, refusal, reasoning content, audio transcript and function or tool-call arguments; every Responses `*.delta` event; Anthropic text, `partial_json`, thinking and citations): the delta text is rebuilt per stream, and the marker replaces the secret in the first delta it touches while the rest lose their part, so the events stay valid and replayable. A response is a stream when its media type is `text/event-stream` (with any parameters) or the request set `"stream": true`; a stream that does not parse fails the capture instead of being written.

Check a fixture before committing it, for example in CI with the same secrets in the environment:

```bash
uv run attest capture --check-redaction tests/attest/fixtures/inc-1042.attest.json
```

It prints the JSON path of each hit (never the secret; an object key holding one is shown as `<redacted key #N>`) and exits 1 when there is one.

### Templating

Values that differ between two runs of the same scenario become stable placeholders, the same placeholder for the same value across the trace, the exchanges and the keys:

| Value | Becomes |
| --- | --- |
| trace and span ids | `0000…01`, `0000…02`, … (same width; a prefix such as `otel_` is kept) |
| provider response, item and tool-call ids (`chatcmpl-`, `resp_`, `msg_`, `call_`, `toolu_`, `fc_`, `rs_`, with a digit after the prefix) | the prefix plus `{{id_N}}` |
| emails | `{{email_N}}` |
| timestamps: numeric fields (`started_at_ms`, `ended_at_ms`, `created`, `created_at`, `startTimeUnixNano`, …) and ISO-8601 strings anywhere | a sequence number by position, never by value: the fixture is walked with arrays in order and, in each object, start-like keys first, other keys by name, end-like keys last. Numeric fields become the number and text becomes `{{timestamp_N}}`. Every start precedes its end, a parent's start and end bracket its children, and runs that differ only in timing write the same bytes |
| durations (`latency_ms`, `duration_ms`, …) | `0` |

Keys are sorted and indentation is fixed, so capturing the same scripted run twice writes byte-identical fixtures, and a re-capture diff shows only what the agent did differently.

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | The fixture is verified (`--check-redaction`: no secret found). |
| 1 | The fixture is not verified (it is still written), no fixture could be built (no spans, an unfinished model call, a malformed export), or `--check-redaction` found a secret. |
| 2 | Usage error. |

`attest trace check <fixture>` checks a fixture: it decodes, its trace passes the engine's trace validation, and its capture-time trace check was clean; an unverified fixture is a warning.

## Limits

The listener accepts loopback clients only: requests whose `Host` is not `localhost` or a loopback address, and requests with an `Origin` header (a web page), are refused. It serves only `/<token>/openai/v1/…`, `/<token>/anthropic/…` and `/<token>/v1/traces`, admits 64 concurrent requests, refuses request bodies over 32 MiB, records at most 32 MiB of each response (marked `truncated`) and 512 MiB per capture (past which the capture fails), and holds at most 20,000 spans; OTLP protobuf bodies are checked against the span, attribute and nesting limits before they are decoded. An agent whose model client cannot change its base URL still gets its trace and drafted contracts through the receiver; its fixture has no exchanges.
