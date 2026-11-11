# Capture: turn a run into a regression fixture

`attest capture` runs your agent once and saves its trace and model exchanges as a regression fixture: `tests/attest/fixtures/<name>.attest.json`. It applies secret redaction, drafts deterministic contracts, checks them against the recording, and writes a test. Capture an incident to reproduce it, or a good run to guard its behavior; review the contracts and fixture before committing either.

The command runs in `attest-engine`; `uv run attest capture …` and `npx attest capture …` forward to the engine of the installed SDK version and then draft the test file in that SDK's language.

Capture is a live recording, not an offline test: the command can call providers and change external systems. Use a [world fixture](world-fixtures.md) or a disposable environment for side effects. Redaction is pattern-based, not a guarantee that all sensitive data is removed; [check the fixture](#secrets) before sharing it. A refresh asks before running the recorded command, and generated command-based tests run a reviewed literal `COMMAND` under strict replay rather than executing arbitrary argv from the fixture.

## Capture a command

<!-- snippet: skip reason="runs the project's own incident command (make incident, npm run incident)" -->
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
  consistent with the recording: incident reproduction; expected failure: cardinality-issue_refund-per-order_id
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

## Incident reproductions and runs consistent with the recording

"Consistent with the recording" is what capture establishes: the drafted contracts hold on the run that was captured. It does not establish that the run was correct. A normal capture of a double refund drafts cardinality 2 and reports the fixture consistent; pick the invariants you rely on yourself, and use `--expect-fail` when the run is an incident. The `verified` field and the `verified` outcome in `--json` output keep their names and carry this meaning.

Capture drafts these contracts from the run:

| Contract | Drafted as |
| --- | --- |
| `tools-called` | every tool the run called is called (`trajectory(..., mode="superset")`) |
| `order-<a>-before-<b>` | each tool's first call keeps its order (`tool_called_before`) |
| `cardinality-<tool>-per-<arg>` | each tool called with an `id` or `*_id` argument is called at most N times per value of it (`tool_cardinality(tool, per=arg, at_most=N)`; N below) |
| `output-schema` | the structured output keeps its keys and their JSON types (`output_matches_schema`) |
| `cost-ceiling` | the run costs at most 1.5× the observed `metadata.cost_usd` (`cost_under`) |

Cardinality depends on the capture mode. A normal capture sets N to the most calls the run made with any one value, so a healthy agent that reads an order twice still verifies. With `--expect-fail`, N is 1 for every such tool, so the calls the incident repeated fail: capturing INC-1042 that way fails only `cardinality-issue_refund-per-order_id` (it refunded order O-1042 twice) and records it as the expected failure. A normal capture of the same run drafts `issue_refund` at 2 and verifies, since without `--expect-fail` capture cannot know the run is an incident.

- Without `--expect-fail`, the fixture is **consistent with the recording** when at least one contract was drafted, every drafted contract passes and the captured trace passes trace check. It is **not verified** (`attest capture` exits 1, naming every cause that holds, joined by `;`) when a drafted contract fails, the trace check failed, no `llm_call` or `tool_call` step was mapped, or nothing was drafted from a run whose command exited non-zero or whose replay-capable capture recorded no model call. A non-zero exit and a missing model call are named among the causes; on their own they do not fail a capture whose drafted contracts pass (the command's exit code is recorded, not propagated). A capture that drafted nothing is never reported verified.
- A healthy run with nothing to draft from (the command succeeded, the trace check is clean, model calls were recorded, but the trace has no tool call, structured output or cost) has the outcome `nothing_to_verify`: the fixture is written with `verified: false`, `attest capture` exits 0 and prints `captured: nothing to verify (no tool call, structured output or cost); replay-only fixture, no test drafted`, and no test is drafted. The fixture is still usable for replay. With `--expect-fail`, drafting nothing is not verified (exit 1).
- `attest capture --json` and `--result-file` report `"outcome"`: `verified`, `not_verified` or `nothing_to_verify`, next to the `verified` boolean (`true` only for `verified`). Branch on `outcome` or the exit code, not on `verified` alone.
- With `--expect-fail`, the fixture is an **incident reproduction**: consistent with the recording when at least one drafted contract fails and the rest pass. The failing contracts are recorded as `expected_failures`.

## The drafted test

For a fixture that is consistent with the recording, `attest capture` writes `tests/attest/test_<name>.py` (Python; the name lowercased, other characters as `_`) or `tests/attest/<name>.test.ts` (TypeScript) next to the fixture directory, unless that file already exists: re-capturing never overwrites a test you edited. A fixture that is not consistent with the recording gets no test, so capturing again with `--expect-fail` (or after editing the agent) still drafts one.

For a fixture captured from a command, the test runs the agent again. It writes the command as a literal in the test file (`COMMAND`) and runs `attest-engine replay --mode strict --no-live` on it: OpenAI- and Anthropic-compatible calls are answered from the recorded model responses, and a call that matches no recording fails the test. Other providers are not blocked: the replay proxy redirects only `OPENAI_BASE_URL` and `ANTHROPIC_BASE_URL`, and `ATTEST_EGRESS=block` only affects processes that install attest's guard. The helper removes `ANTHROPIC_API_KEY`, `GOOGLE_API_KEY` and `GEMINI_API_KEY` and sets `OPENAI_API_KEY` to a placeholder, but run the drafted tests without any other provider's credentials. The drafted contracts are evaluated on the trace of that new run, not on the recording. That trace is not redacted; the helper keeps it in a private temporary directory and removes it afterwards.

`COMMAND` is what runs, because the fixture is a file a pull request can change: review the literal with the test. The test refuses to run (it fails with a message naming both commands) when the fixture's `source.command` differs from `COMMAND`, and when the command has redacted or templated arguments, as `--refresh` does:

<!-- snippet: fragment reason="excerpt: relies on `Path`, defined outside the snippet" -->
```python
FIXTURE = Path(__file__).parent / "fixtures/inc-1042.attest.json"
RUN_DIR = Path(__file__).parent / "../.."  # where `attest capture` ran
WORLD = None  # the --world file of the capture, when it had one
COMMAND = ["python", "agent.py"]  # reviewed argv: the only command the test runs


def contracts(trace: Trace) -> ExpectChain:
    return (
        expect(trace)
        # cardinality-issue_refund-per-order_id: issue_refund at most once per order_id
        .tool_cardinality("issue_refund", per="order_id", at_most=1)
        # ...
    )


def test_inc_1042_new_run_consistent_with_recording(attest: AttestEngineFixture) -> None:
    trace = replay_run(FIXTURE, command=COMMAND, cwd=RUN_DIR, world=WORLD)

    attest.evaluate(contracts(trace))
```

The test fails when the agent regresses: a changed prompt, schema or conversation is a replay miss that names the first component that changed, a changed run order or count fails its contract, and a command that exits differently than when it was recorded fails. A pass means the new run is consistent with the recording and satisfies the drafted contracts; it does not prove the agent correct. The TypeScript file does the same with `attestExpect`, `replayRun` and `evaluate` from `@attest-ai/core` and `@attest-ai/vitest`.

An incident fixture (`--expect-fail`) gets the same test. Its contracts failed on the recording, so the test passes once the new run satisfies them. Fixing the agent usually changes the conversation, so replaying the fixed agent against the incident's recording misses; re-record with `attest capture --refresh <name>` (or capture the fixed agent) to get a recording the fixed agent replays.

The test needs the command to run unchanged: it uses the working directory `attest capture` ran in and the `--world` file the capture was given. A fixture built `--from` an OTLP file or by `record()` has no command to re-run; its test loads the fixture and runs the contracts against the recorded trace without running the agent. For an incident reproduction it uses `inspect()` and asserts each contract's recorded status, so it stays green while it proves the contract catches the incident; otherwise it uses `evaluate()`.

## Refresh a fixture

`attest capture --refresh inc-1042` prints the command recorded in the fixture (`source.command`), asks for confirmation (`y` at a terminal; without one, `--yes`), then runs it, with the flags it was captured with (`--expect-fail` included), and overwrites the fixture. **Review the printed command before confirming a fixture you did not record:** the fixture is a file in your repository, and whoever can change it can change the command that `--refresh` runs. Run it from the directory the original capture ran in, with the environment the command needs. It refuses with an explanation when the command's arguments were redacted (a secret passed as an argument), and for fixtures captured with `--from` or in process, which have no command; re-run those captures yourself. It also refuses a fixture with a `world` section, because a refresh would drop the world's state: re-run `attest capture --name inc-1042 --world <world.yaml> …` instead. A replay miss names this command (see `attest-engine replay`).

### Review what the refresh changed

A refresh replaces the fixture, and a byte diff of the JSON hides what mattered. After the capture summary, `--refresh` compares the fixture it replaced with the one it wrote, exchange by exchange (paired by position; extra or missing exchanges are reported as added or removed), and prints what changed in the requests the agent sent. Edit the system prompt, refresh, and the changed lines show per exchange:

```text
refresh refund: 1 of 1 exchanges changed
  exchange 0 (openai chat_completions gpt-4.1): changed
    system prompt:
      - You refund once.
      + You refund twice.
```

Unchanged lines are not printed. The comparison covers the system prompt (changed lines only), tool schemas (tools added, removed or changed, with the changed JSON paths), messages and tool results (added, removed or changed, with role and the changed lines), the model and the request parameters:

```text
refresh refund: 1 of 1 exchanges changed
  exchange 0 (openai chat_completions gpt-5.4): changed
    model: gpt-4.1 -> gpt-5.4
    messages:
      ~ user [0]
        - Refund order O-2077.
        + Refund order O-2077 in full.
```

Chat Completions, Responses and Messages requests are normalized to the same form. Each block prints at most 20 lines, then `... N more changed lines elided`. Responses and the trace are not compared. The diff is read from the redacted fixtures as written and every string passes through the capture redactor again, so a secret in a prompt is never printed; control characters are escaped, so a line break in a name cannot forge a diff line.

A refresh with no request-level semantic change prints one line. This does not guarantee identical fixture bytes: model responses, trace evidence and framework-internal ids are outside the semantic comparison.

```text
refresh refund: no semantic change
```

The report goes to stderr. With `--json`, the result gains a `diff` object (`cli/capture.schema.json`): `changed` and `exchanges`, which lists only the exchanges that differ. Each has `index`, `change` (`added`, `removed` or `changed`), `label` (provider, API and model) and any of `model` (`old`, `new`), `system` (`{op, text}` lines, `-` old and `+` new), `tools`, `messages` and `tool_results` (`index`, `change`, `role`, `lines`, and `fields` for changed fields the lines do not show), `params` and `other` (changed JSON paths). An unchanged refresh reports `{"changed": false, "exchanges": []}`.

## Capture in process

When the agent runs inside your own process (a notebook, a script, a test), capture a block of code:

<!-- snippet: fragment reason="excerpt: relies on `OpenAI`, defined outside the snippet" -->
```python
import attest

with attest.record("inc-1042", expect_fail=True) as capture:
    client = OpenAI()           # construct clients inside the block
    run_refund_agent(client, "O-1042")

print(capture.result.fixture, capture.result.verified)
```

<!-- snippet: fragment reason="excerpt: relies on `OpenAI`, defined outside the snippet" -->
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

If the block raises, the capture is abandoned and no fixture is written. A fixture that is not verified raises `attest.CaptureError` (TypeScript: `CaptureError`) after it is written, and no test is drafted. A `nothing_to_verify` capture does not raise: `capture.result.verified` is `False` and `capture.result.outcome` is `"nothing_to_verify"` (TypeScript: `verified: false`, `outcome: "nothing_to_verify"`), and no test is drafted.

Options: `out_dir` / `outDir` (default `tests/attest/fixtures`), `expect_fail` / `expectFail`, `agent_id` / `agentId`, `emit_test` / `emitTest` (default true), `engine_path` / `enginePath`, `world` (a `world.yaml` to host for the block; see [world fixtures](world-fixtures.md)), and in TypeScript `flush`. `record()` sets process-wide variables, so captures cannot overlap; a second `record()` while one runs raises `CaptureError`.

## Capture from an export

<!-- snippet: skip reason="needs the INC-1042 trace export from a real incident capture (the refund-desk demo repository)" -->
```bash
uv run attest capture --name inc-1042 --expect-fail --from incidents/INC-1042/trace.py.otlp.json
```

`--from` builds the fixture from an OTLP/JSON export. It has the trace and the drafted contracts but no model exchanges, so it is marked `"replay_capable": false`.

## Frameworks

Capture reads the spans an agent framework exports through its own OpenTelemetry instrumentation; there is no attest-specific adapter. Each framework below was run on a scripted model server through `attest capture` and produced a verified fixture. The versions are the ones tested. Every route needs the framework to export OTLP/HTTP to `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` (the capture environment sets it) and to flush its exporter before the process exits.

| Framework | Instrumentation tested | Model API |
| --- | --- | --- |
| Pydantic AI (Python) | native, `pydantic-ai-slim` 2.65.0 | Chat Completions |
| Vercel AI SDK (TypeScript) | native `@ai-sdk/otel` 1.0.162, `ai` 7.0.162 | Responses (default) or Chat Completions |
| Mastra (TypeScript) | native `@mastra/otel-exporter` 1.5.0, `@mastra/core` 1.81.0 | Chat Completions |
| OpenAI Agents SDK (Python) | `opentelemetry-instrumentation-openai-agents-v2` 0.1.0 (alpha), `openai-agents` 0.24.0 | Responses |
| Claude Agent SDK (Python, TypeScript) | Claude Code native traces (beta), Claude Code 2.1.317 | Anthropic Messages (streaming) |
| LangGraph (Python) | LangSmith 0.14.17 in OTel mode, `langgraph` 1.2.19 | Chat Completions |

All of them read `OPENAI_BASE_URL` or `ANTHROPIC_BASE_URL` from the environment, so the recording proxy needs no code change in the agent.

**Pydantic AI.** Pass the native instrumentation capability an OTLP/HTTP tracer provider and pin the instrumentation version to 5:

<!-- snippet: fragment reason="excerpt: relies on `TracerProvider`, defined outside the snippet" -->
```python
provider = TracerProvider()
provider.add_span_processor(SimpleSpanProcessor(OTLPSpanExporter()))
settings = InstrumentationSettings(tracer_provider=provider, include_content=True, version=5)
agent = Agent(OpenAIChatModel("gpt-4.1", provider=OpenAIProvider()), capabilities=[Instrumentation(settings)])
# ... agent.run_sync(...)
provider.force_flush()
```

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
uv add "pydantic-ai-slim[openai]" opentelemetry-sdk opentelemetry-exporter-otlp-proto-http
```

Version 3 also works. Version 2 names tool spans `running tool` and keeps arguments outside the GenAI keys, so the drafted contracts lose per-id cardinality.

**Vercel AI SDK.** AI SDK 7 needs the integration registered, in addition to `experimental_telemetry`:

<!-- snippet: fragment reason="excerpt: relies on `NodeTracerProvider`, defined outside the snippet" -->
```ts
const provider = new NodeTracerProvider({ spanProcessors: [new SimpleSpanProcessor(new OTLPTraceExporter())] });
provider.register();
registerTelemetry(new OpenTelemetry()); // from @ai-sdk/otel
// generateText({ ..., experimental_telemetry: { isEnabled: true } })
await provider.forceFlush();
```

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
npm install ai @ai-sdk/openai @ai-sdk/otel @opentelemetry/sdk-trace-node @opentelemetry/exporter-trace-otlp-http
```

Tested with `@opentelemetry/sdk-trace-node` 2.12.0 and `@opentelemetry/exporter-trace-otlp-http` 0.219.0; newer exporter releases require an OTel API the 2.12.0 provider does not accept. The legacy integration (`LegacyOpenTelemetry`, spans named `ai.generateText` and `ai.toolCall`) is not supported: it records no `gen_ai.operation.name`, so capture finds no model or tool step and refuses to verify.

**Mastra.** Register an `OtelExporter` with a custom OTLP/HTTP endpoint and flush the observability instance:

<!-- snippet: fragment reason="excerpt: relies on `Observability`, defined outside the snippet" -->
```ts
const observability = new Observability({ configs: { otel: { serviceName: "my-agent", exporters: [new OtelExporter({
  provider: { custom: { endpoints: { traces: process.env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT }, protocol: "http/json" } },
  signals: { traces: true, logs: false },
})] } } });
const mastra = new Mastra({ agents: { agent }, observability });
// ... await agent.generate(...)
await observability.flush();
```

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
npm install @mastra/core @mastra/observability @mastra/otel-exporter @ai-sdk/openai
```

Use `createOpenAI().chat(...)` for the model, which Mastra's exporter reports as `openai.chat`. Mastra records finish reasons as the JSON text `["tool-calls"]`; trace check reads that form.

**OpenAI Agents SDK (Python).** Use the OpenTelemetry project's instrumentation, not OpenInference, and remove the SDK's hosted trace processors first:

<!-- snippet: fragment reason="excerpt: relies on `set_trace_processors`, defined outside the snippet" -->
```python
set_trace_processors([])
OpenAIAgentsInstrumentor().instrument(tracer_provider=provider, capture_message_content=True)
```

<!-- snippet: skip reason="installs packages (network)" -->
```bash
uv add openai-agents opentelemetry-instrumentation-openai-agents-v2 opentelemetry-sdk opentelemetry-exporter-otlp-proto-http
```

Then set:

```bash
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true OTEL_INSTRUMENTATION_OPENAI_AGENTS_CAPTURE_METRICS=false
```

The instrumentation is alpha. It emits `unknown` spans for SDK internals; they are not steps. Its tool spans carry no `gen_ai.tool.call.id`, so trace check pairs them by order.

**Claude Agent SDK (Python and TypeScript).** The SDK runs Claude Code, whose native traces are in beta and are off unless both switches are set. Set them in the environment of the process that runs the SDK:

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1 CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1
export OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_PROTOCOL=http/json OTEL_TRACES_EXPORT_INTERVAL=100
export OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none OTEL_LOG_TOOL_DETAILS=1 OTEL_LOG_TOOL_CONTENT=1
```

Without `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA` Claude Code exports metrics and logs only. Capture maps `claude_code.llm_request` to a model call (model, `input_tokens`, `output_tokens` and cache tokens come from its native attributes), `claude_code.tool` to a tool call whose result is the `output` of its `tool.output` event, and `claude_code.interaction` to the turn. `claude_code.tool.execution` and `claude_code.tool.blocked_on_user` add no step. Claude Code records neither tool arguments nor message content, so the drafted contracts cover the tools called and their order but not per-id cardinality. `OTEL_LOG_TOOL_CONTENT=1` puts each tool's output (file contents, shell output) into the fixture as recorded, with only pattern-based secret redaction applied. Run `attest capture --check-redaction <fixture>` and read the fixture before committing it, or leave `OTEL_LOG_TOOL_CONTENT` unset to keep tool outputs out. Because the traces are a beta, attribute names can change between Claude Code releases.

**LangGraph (Python).** Use LangSmith in OTel mode with a tracer provider you own, so spans go to the capture receiver and nowhere else:

<!-- snippet: fragment reason="excerpt: relies on `Client`, defined outside the snippet" -->
```python
client = Client(tracing_mode="otel", otel_tracer_provider=provider)
with tracing_context(enabled=True, client=client):
    graph.invoke(...)
provider.force_flush()
```

<!-- snippet: skip reason="installs packages (network)" -->
```bash
uv add langgraph langchain-openai langsmith opentelemetry-sdk opentelemetry-exporter-otlp-proto-http
```

Then set:

```bash
export LANGSMITH_TRACING=true LANGSMITH_TRACING_MODE=otel LANGSMITH_API_KEY=unused
```

LangSmith exports every runnable as a span with `gen_ai.operation.name=chain`; capture ignores those and keeps the `chat` and `execute_tool` spans. Tool arguments and result arrive in `gen_ai.prompt` and `gen_ai.completion`; capture reads the argument object and the tool message's `content`. Export only to the local provider: LangSmith must run in OTel mode with your own exporter, and the key is never used.

OpenAI Agents SDK and LangGraph for TypeScript have no instrumentation that emits GenAI spans yet, and OpenInference spans (`openinference.span.kind`, `llm.*`) are not mapped.

### When capture reports no steps

A capture where no `llm_call` or `tool_call` step was mapped is not verified (exit 1): contracts over a trace without them pass vacuously. Either no span carries a `gen_ai.operation.name` that attest maps (`chat`, `execute_tool`, ...), or only agent and retrieval spans do. Check the framework's instrumentation against the table above. `attest trace check <export>` reports `no_genai_spans` when no span at all is mapped.

## The fixture

A fixture follows `proto/attest/v1/fixture-v1.schema.json`:

| Field | Holds |
| --- | --- |
| `fixture_version` | the fixture format version, `1` |
| `name` | the fixture name |
| `source` | `kind` (`command`, `in_process`, `otlp_file`), `replay_capable`, and for a command its arguments and exit code |
| `trace` | the run as an Attest trace (schema version 2), as `attest trace convert` writes it |
| `exchanges` | every model call in request order: provider, API (`chat_completions`, `responses`, `messages`, `other`), path, request body, response status, content type and body (the JSON value, or the event-stream text for a streamed call), and its `key` |
| `contracts` | the drafted contracts as engine assertion specs |
| `verification` | `verified`, `expect_fail`, the capture-time trace check, each contract's status and explanation, and `expected_failures` |
| `world` | the final state of the [world](world-fixtures.md) hosted with `--world` (`world_version`, `definition_sha256`, `state`, `faults_fired`, `faults_fired_count`), redacted like the rest of the fixture, with email addresses templated to the trace's `{{email_N}}` tokens (ids and timestamps in records are kept as they were); evidence only, never fed back into a replay's world; `null` for a capture without `--world` |

Each exchange's `key` identifies the request: the `model`, the request parameters other than the prompt and the tools (`params`), and SHA-256 hashes of the prompt (`messages`; `input` and `instructions`; `system` and `messages`) and of the tool schemas. [Replay](replay.md) recomputes these keys to match new requests to recorded responses.

### Secrets

Request headers, which carry the API key, are never recorded. Everywhere else in the fixture, including object keys, capture replaces with `[redacted]`:

- the value of every environment variable whose name ends in `KEY`, `TOKEN`, `SECRET`, `PASSWORD`, `_PASS`, `_PWD`, `PASSWD`, `CREDENTIALS`, `CREDENTIAL`, `DSN` or `AUTH` (values of eight characters or more; `PWD`, `OLDPWD` and `*_ASKPASS` are paths, not secrets), and the password of any environment value that is a URL with userinfo. For `record()` this includes variables the block sets itself, such as keys loaded from a dotenv file;
- strings shaped like known credentials: `sk-…` (OpenAI, Anthropic), `sk_live_` / `sk_test_` / `rk_live_` (Stripe), `gsk_` (Groq), `hf_` (Hugging Face), `xai-`, `AKIA…`, `AIza…`, `ghp_` / `gho_` / `ghs_` / `github_pat_`, `xox…-`, `Bearer …`, JWTs, PEM private keys, and the password in any `scheme://user:password@`. Azure (32 hex characters), Mistral (32) and Cohere (40) keys have no prefix, so they are matched by the name that introduces them: `api-key`, `subscription-key`, `access-key`, `client_secret` or `x-api-key` (a `-` or `_` between the words, and `ocp-apim-subscription-key`, match too), as a JSON key, a header or a query parameter, and a value of more than 64 characters is redacted whole. A bare 32-hex string is kept, because trace ids and content hashes look the same;
- the command line is recorded in the fixture. The values of flags whose name contains `key`, `token`, `secret`, `password`, `pass`, `auth` or `credential` (`--api-key=…`, `--api-key …`, `-token …`) and of `NAME=value` arguments whose name is a secret by the environment name rule above, whatever the value's length (`env DB_PASSWORD=… make incident`) are redacted; one-letter flags (`-p …`) are recorded as they are, so pass secrets in the environment, not on the command line. The values of `key`, `api_key`, `apikey`, `api-key`, `token`, `access_token`, `access-key`, `subscription-key`, `client_secret`, `sig` and `signature` query parameters in exchange paths are redacted too; paths are stored percent-decoded;
- secrets a streamed response spells out across several deltas (Chat Completions content, refusal, reasoning content, audio transcript and function or tool-call arguments; every Responses `*.delta` event; Anthropic text, `partial_json`, thinking and citations): the delta text is rebuilt per stream, and the marker replaces the secret in the first delta it touches while the rest lose their part, so the events stay valid and replayable. A response is a stream when its media type is `text/event-stream` (with any parameters) or the request set `"stream": true`; a stream that does not parse fails the capture instead of being written.
- every other server-sent event: capture decodes each event's JSON and redacts every string in it, not only the delta text, so a secret in `response.completed` is redacted even when it holds `"` or `\` (which the raw event spells as `\"` or `\\`). The same applies to a secret escaped twice, such as JSON arguments inside a JSON string. An event with nothing to redact keeps its original bytes;
- the secret values in the `.env` file of the working directory. Capture reads it with dotenv semantics (`export NAME=value`, quoted and multi-line values, `#` comments) and applies the environment name rule above, so `OPENAI_API_KEY=…` and a `DATABASE_URL` with a password become redaction targets. The values are used only to find secrets: they are never written to the fixture. A missing `.env` is ignored. **Behavior change:** `attest capture` and `attest capture --check-redaction` fail when `.env` is not a regular file (a symlink to one is followed) or is larger than 1 MiB, rather than capture without its secrets. `attest replay` reads it for the same purpose when it redacts a live request before matching; it warns once on stderr and ignores an unusable `.env`.

Check a fixture before committing it, for example in CI with the same secrets in the environment:

<!-- snippet: setup=project,incident -->
```bash
uv run attest capture --check-redaction tests/attest/fixtures/inc-1042.attest.json
```

It prints the JSON path of each hit (never the secret; an object key holding one is shown as `<redacted key #N>`, and a hit inside a streamed body adds `(event N)` to the path of the body) and exits 1 when there is one. It scans decoded event JSON, so it flags a secret that capture would have JSON-escaped, and it checks against the process environment and the working directory's `.env`: run it from the directory the capture ran in.

### Templating

Values that differ between two runs of the same scenario become stable placeholders, the same placeholder for the same value across the trace, the exchanges and the keys (and, for email addresses, the `world` section):

| Value | Becomes |
| --- | --- |
| trace and span ids | `0000…01`, `0000…02`, … (same width; a prefix such as `otel_` is kept) |
| provider response, item and tool-call ids (`chatcmpl-`, `resp_`, `msg_`, `call_`, `toolu_`, `fc_`, `rs_`, with a digit after the prefix) | the prefix plus `{{id_N}}` |
| emails | `{{email_N}}` |
| conversation ids (`trace.conversation_id`, the `gen_ai.conversation.id` span attribute; a tool argument named `conversation_id` stays literal), which frameworks often derive from the clock | `{{conversation_N}}`, numbered by first occurrence |
| timestamps: numeric fields (`started_at_ms`, `ended_at_ms`, `created`, `created_at`, `startTimeUnixNano`, …) and ISO-8601 strings anywhere | a sequence number by position, never by value: the fixture is walked with arrays in order and, in each object, start-like keys first, other keys by name, end-like keys last. Numeric fields become the number and text becomes `{{timestamp_N}}`. Every start precedes its end, a parent's start and end bracket its children, and runs that differ only in timing write the same bytes |
| durations (`latency_ms`, `duration_ms`, …) | `0` |

Keys are sorted and indentation is fixed, so capturing the same scripted run twice writes the same bytes in every field capture controls: the values above, and key order. A re-capture diff shows what the agent did differently, plus the framework-internal ids below.

Capture does not template ids a framework invents inside values it owns, so these differ between two captures of the same run and show up in a re-capture diff:

- raw span ids in `turns[].from_agent` (OpenAI Agents);
- LangChain message ids and `lc_run--` UUIDv7 ids inside the LangSmith prompt and completion strings (LangGraph).

Replay keys are unaffected by these. `keyOf` hashes only an exchange's templated request (model, params, normalized prompt fields and tool-schema hash), and these ids live in the trace, not in the request sent to the provider.

One framework puts per-run values inside the request:

- Claude Agent SDK (Claude Code): the Anthropic request carries `metadata.user_id`, which holds the device id, the account uuid and the session id (a JSON string, or the older `user_<device>_account_<uuid>_session_<uuid>` form). Capture templates the three values to `{{device_N}}`, `{{account_N}}` and `{{session_N}}`, replaces them wherever else they appear in the fixture, and writes none of them. `metadata.user_id` is left out of the replay key and of the miss diagnosis, so a replay in a new session hits.
- Claude Code also describes the run to the model in its system prompt (`# Environment`) and in `<system-reminder>` blocks of the user turn: working directory, platform, shell, OS version, the model name and knowledge cutoff, today's date (`# currentDate`), the git status (`gitStatus:`: branch, status, recent commits), the entries under `Additional working directories:`, and `<total_tokens>` lines. Replay matching neutralizes those values, line by line, in Claude Code's own sections only; the stored request is unchanged. Any other line of a reminder (instructions, project memory) still counts toward the key, and a `Status:` or date line in your application's own prompt keeps its meaning.

**Known limit:** the contents of a `tool_result` are never normalized, because a tool result is data the agent saw, not Claude Code's own text. A volatile value that reaches the request through a tool result, for example a `<total_tokens>` reminder Claude Code appends inside one, is part of the key, so a replay whose value differs from the recording is a miss on the prompt. Re-capture with `--refresh`, or keep that value out of tool output.

A fixture recorded before this change keeps replaying, because keys are recomputed when the fixture is loaded. It still holds the identifiers it was captured with: re-capture it with `attest capture --refresh` to write the templated form, and review the diff before committing.

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | The fixture is verified or captured with nothing to verify (`--check-redaction`: no secret found). |
| 1 | The fixture is not verified (it is still written): a drafted contract failed, the trace check failed, no step was mapped, `--expect-fail` drafted nothing, or nothing was drafted while the command exited non-zero or a replay-capable capture recorded no model call; no fixture could be built (no spans, an unfinished model call, a malformed export), or `--check-redaction` found a secret. |
| 2 | Usage error. |

`attest trace check <fixture>` checks a fixture: it decodes, its trace passes the engine's trace validation, and its capture-time trace check was clean; an unverified fixture is a warning.

A pull request that changes a fixture can run just the tests that reference it by name: see [selection](selection.md).

## Limits

**Strict replay keeps relative identity, not absolute identity.** Email addresses are numbered across the whole replay session in the order the run first sends them, so a request whose identities play different roles than in the recording misses: a recording that looked up and notified the same customer does not replay for a run that looks up customer A and notifies customer B. A run that uses one different address throughout (customer B instead of customer A) still replays, because the roles are unchanged. Replay cannot tell that the run's address differs from the recorded one, so it does not detect a regression that replaces every use of one identity with another. Ids and timestamps are renumbered per request and carry no identity.

The listener accepts loopback clients only: requests whose `Host` is not `localhost` or a loopback address, and requests with an `Origin` header (a web page), are refused. It serves only `/<token>/openai/v1/…`, `/<token>/anthropic/…` and `/<token>/v1/traces`, admits 64 concurrent requests, refuses request bodies over 32 MiB, records at most 32 MiB of each response (marked `truncated`) and 512 MiB per capture (past which the capture fails), and holds at most 20,000 spans; OTLP protobuf bodies are checked against the span, attribute and nesting limits before they are decoded. An agent whose model client cannot change its base URL still gets its trace and drafted contracts through the receiver; its fixture has no exchanges.
