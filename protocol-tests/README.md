# Attest Protocol Conformance Suite

Cross-SDK fixtures that exercise the JSON-RPC reader path of every Attest SDK. Each fixture replays a sequence of NDJSON lines through the SDK's `handleLine` / `_handle_line` entry point and asserts on the resulting protocol diagnostics.

The Python and TypeScript SDKs both consume the same fixtures so that protocol observability stays consistent across implementations.

## Layout

```
protocol-tests/
├── README.md
├── conformance.schema.json     # JSON Schema for fixture entries
└── fixtures/
    ├── 001-malformed-json.json
    ├── 002-non-object-response.json
    └── ...
```

Each fixture is a JSON object with the following fields:

| Field | Type | Description |
|---|---|---|
| `name` | string | Human-readable scenario identifier. |
| `description` | string | What this scenario verifies. |
| `lines` | string[] | Raw NDJSON lines (without trailing newline) to replay through the reader, in order. |
| `prePending` | int[] | Optional. Pending request IDs to register before replay so unknown-id vs. routed-error paths can be exercised. |
| `expectedDiagnostics` | object[] | Ordered list of expected diagnostics. Each entry has `kind` (one of `malformed_json`, `non_object_response`, `invalid_jsonrpc_version`, `missing_id`, `unknown_id`, `non_routable_error`) and an optional `messageContains` substring. |
| `expectedDesync` | bool | Whether the scenario should leave the client in a desynced state. |

## Running

The Python SDK runs the suite via `tests/test_protocol_conformance.py`; the TypeScript SDK via `tests/conformance/protocol-conformance.test.ts`. Both implementations expand `../../protocol-tests/fixtures/*.json` at test collection time so adding a new fixture lights up both SDKs at once.

To add a new scenario:

1. Drop a new `NNN-name.json` file into `fixtures/` matching the schema.
2. Run the Python and TypeScript suites locally to confirm both observe the expected diagnostic sequence.
3. Commit the fixture and the (possibly empty) implementation update.
