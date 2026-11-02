# Trace check: integrity and coverage of an OTLP export

`attest trace check` reads an OpenTelemetry trace export (OTLP/JSON, the `ExportTraceServiceRequest` shape that the OTLP file exporter and collectors write) and reports whether its structure can be trusted and which evidence it lacks. `attest trace convert` turns the same export into an Attest trace (schema version 2) that assertions run against.

Both commands run in `attest-engine`; `uv run attest trace …` and `npx attest trace …` forward to the engine of the installed SDK version.

## Check an export

```bash
uv run attest trace check incidents/INC-1042/trace.py.otlp.json
```

```text
trace check incidents/INC-1042/trace.py.otlp.json
trace 29478f3e0a07ff5725faf3112a8e3663: 10 spans, attest trace schema v2
steps: 5 llm_call, 4 tool_call; 1 turn, 0 handoffs, 1 retry
structure: OK
coverage: missing evidence
  cost     5 of 5 llm_call spans lack gen_ai.usage.cost
  content  5 of 5 llm_call spans lack gen_ai.input.messages / gen_ai.output.messages
```

`--json` prints the same report as JSON (`ok`, `issues`, `coverage`, step counts). Pass `-` to read the export from stdin.

| Exit | Meaning |
| --- | --- |
| 0 | No integrity errors. Warnings and coverage gaps do not fail the check. |
| 1 | At least one integrity error, or the input is not a readable OTLP/JSON export. |
| 2 | Usage error. |

## Integrity errors

Each error names the span id it concerns.

| Code | Meaning |
| --- | --- |
| `orphan_span` | The span's `parentSpanId` is not in the trace. |
| `parent_cycle` | Following parents from the span leads back to it. |
| `duplicate_span_id` | Two spans share an id. |
| `unpaired_tool_call` | A model span finished with `tool_calls` (`gen_ai.response.finish_reasons`) but no tool span executes the call, or a tool call id requested in `gen_ai.output.messages` has no tool span with that `gen_ai.tool.call.id`. |
| `unpaired_tool_result` | A tool span follows a model span that did not request tools, or carries a `gen_ai.tool.call.id` that no recorded model output requested. |
| `multiple_traces` | The file holds more than one trace id; check one trace per file. |
| `conversion_failed` | The spans cannot become an Attest trace, for example a non-numeric `gen_ai.usage.input_tokens`. |

Warnings flag suspicious parentage without failing: `outside_parent` (a span runs outside its parent's time range), `end_before_start`, `multiple_roots`, and `no_genai_spans` (no span carries GenAI attributes).

## Coverage

Coverage counts spans that lack evidence some assertions need:

| Field | Span kind | Attributes |
| --- | --- | --- |
| `tokens` | model calls | `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens` |
| `cost` | model calls | `gen_ai.usage.cost` (USD; not yet part of the GenAI conventions) |
| `content` | model calls | `gen_ai.input.messages` and `gen_ai.output.messages` (or the deprecated `gen_ai.prompt` / `gen_ai.completion`) |
| `model` | model calls | `gen_ai.response.model` or `gen_ai.request.model` |
| `tool_call_id` | tool calls | `gen_ai.tool.call.id` |
| `tool_content` | tool calls | `gen_ai.tool.call.arguments`, `gen_ai.tool.call.result` |

An assertion whose evidence is absent does not fail: it reports `not_evaluated` with reason `missing_evidence`. For example `cost_under(0.05)` on a trace converted from an export without `gen_ai.usage.cost` reports `not_evaluated: missing_evidence (metadata.cost_usd absent)`. Message content is opt-in in most GenAI instrumentations (for OpenTelemetry Python, `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true`).

## Convert an export

```bash
uv run attest trace convert trace.otlp.json > trace.json
```

The engine maps spans by `gen_ai.operation.name` (`chat`, `text_completion`, `generate_content`, `embeddings` → `llm_call`; `execute_tool` → `tool_call`; `invoke_agent` → `agent_call`; `retrieval` → `retrieval`). Spans without GenAI attributes, such as HTTP client spans, are not steps. Trace v2 adds, all optional:

| Field | Source |
| --- | --- |
| `turns` | One turn per top-level `invoke_agent` span, with its input and output messages and the indices of its steps. |
| `handoffs` | An `invoke_agent` span nested under another agent: `from_agent`, `to_agent` and the `agent_call` step. |
| `steps[].retry_of` | The index of the failed step this one repeats: the same tool with the same arguments after a failed call, or a model call directly after a failed model call, under the same parent. |
| `steps[].error` | The span's error status with `error.type` and the status message. |
| `steps[].exchange` | The recorded model request and response (`gen_ai.input.messages`, `gen_ai.output.messages`, models, finish reasons) when both sides were captured. |
| `steps[].span_id`, `parent_span_id`, `tool_call_id` | Provenance from the export. |
| `conversation_id` | `gen_ai.conversation.id`. |

Tool steps take their `args` and `result` from the JSON in `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result`. `metadata.total_tokens` and `metadata.cost_usd` sum the model spans that record them and stay unset when none do; `metadata.latency_ms` is the root span's duration. The schema is `proto/attest/v1/trace-v2.schema.json`.

Input limits: 32 MiB, 20,000 spans, attribute values nested at most 32 levels. Malformed input (invalid ids or timestamps, non-finite numbers, invalid base64, trailing data) is an error naming the span.
