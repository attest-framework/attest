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

## Verdict fixtures

`verdicts/` holds verdict fixtures: an engine `evaluate_batch` result list and the outcome every implementation must derive from it. They cover `pass`, `soft_fail`, `hard_fail` and `not_evaluated` with each reason (`provider_unavailable`, `provider_not_configured`, `simulation`, `missing_evidence`, `short_circuited`). Each fixture matches `verdicts/verdict.schema.json`:

| Field | Description |
|---|---|
| `results` | Assertion results as the engine returns them. |
| `expected.outcome` | Test outcome under the aggregation rules: `pass`, `fail` or `skip`. |
| `expected.strictOutcome` | Outcome with strict mode on (`--attest-strict` / `strict: true`): `not_evaluated` fails instead of skipping. |
| `expected.failedAssertionIds` | `hard_fail` and `soft_fail` rows, in result order. |
| `expected.notEvaluated` | Count of `not_evaluated` rows per reason. |
| `expected.passRate` | `pass / (total - not_evaluated)`; `null` when every row is `not_evaluated`. |
| `expected.softFailWarning` | Whether a soft-failure summary warning is due. |
| `expected.policyExit` | `attest-engine policy evaluate` exit code for these results under `verdicts/policy.yaml`. |

Go checks the wire format and `policyExit` (`engine/internal/policy/verdict_conformance_test.go`); the Python and TypeScript SDKs check the aggregation fields.

## Doctor fixtures

`doctor/` holds examples of `attest-engine doctor --json` output checked against `proto/attest/v1/doctor.schema.json`: every file in `doctor/valid/` must match the schema and every file in `doctor/invalid/` must not. The engine test `engine/cmd/attest-engine/doctor_e2e_test.go` checks the fixtures, validates live `doctor --json` output against the schema and asserts that its `capabilities` equal what `initialize` advertises under the same environment. SDK `attest doctor` commands forward this output unchanged.

## Report fixtures

`report-v2/` holds examples of the report-v2 file (`proto/attest/v1/report-v2.schema.json`) that `pytest --attest-report`, the Vitest `AttestReporter` and the engine report generator write and that `attest policy evaluate` and the pr-annotate action read: every file in `report-v2/valid/` must match the schema and every file in `report-v2/invalid/` must not. Go (`engine/internal/report/report_v2_schema_test.go`, which also validates `GenerateJSONReport` output), Python (`tests/test_report_v2.py`) and TypeScript (`tests/conformance/report-v2-conformance.test.ts`) check them; the SDK tests also validate the reports their test runners write.

## Trace fixtures

`trace-v2/` covers the trace schema version 2 (`proto/attest/v1/trace-v2.schema.json`) and the engine's OTLP/JSON conversion:

- `valid/` and `invalid/`: traces that must and must not match the schema.
- `otlp/<name>.otlp.json` with `otlp/<name>.trace.json`: an OTLP/JSON export and the exact trace `attest-engine trace convert` produces for it. The golden traces must also match the schema. `inc-1042-refund` is the refund-desk INC-1042 incident export.
- `otlp-invalid/`: malformed OTLP/JSON inputs that conversion must reject with an error. They also seed the engine fuzz target.

The engine tests in `engine/internal/trace/` (`trace_v2_schema_test.go`, `otlp_test.go`, `fuzz_test.go`) check them.
