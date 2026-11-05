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

`verdicts/` holds verdict fixtures: an engine `evaluate_batch` result list and the outcome every implementation must derive from it. They cover `pass`, `soft_fail`, `hard_fail` and `not_evaluated` with each reason (`provider_unavailable`, `provider_not_configured`, `simulation`, `missing_evidence`, `short_circuited`, `budget_exhausted`, `judge_abstained`). Each fixture matches `verdicts/verdict.schema.json`:

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

The engine tests in `engine/internal/trace/` (`trace_v2_schema_test.go`, `otlp_test.go`, `fuzz_test.go`) check them. Python checks the schema fixtures and that every golden trace survives `Trace.from_dict(...).to_dict()` unchanged (`tests/test_trace_v2.py`), and that `OTelAdapter.from_otlp` returns each golden trace and rejects each malformed input (`tests/integration/test_otel_engine.py`, with the engine built by `make engine`). TypeScript checks the schema fixtures and runs `OTelAdapter.fromOtlp` against the same golden and malformed files (`tests/conformance/trace-v2-conformance.test.ts`), so both SDKs produce identical trace v2 JSON for the same export.

## Capture fixtures

`fixture-v1/` covers the capture fixture schema (`proto/attest/v1/fixture-v1.schema.json`), which embeds `trace-v2.schema.json` by reference:

- `valid/`: fixtures written by `attest-engine capture` (a command capture of a scripted run through the recording proxy saved with `--expect-fail`, and a trace-only `--from` capture of `trace-v2/otlp/inc-1042-refund.otlp.json`), one with a reserved `world` section, and a verified fixture whose trace has structured output and a cost, so every drafted contract kind appears. The engine test checks that each valid fixture's contracts are exactly what the drafter writes for its trace.
- `invalid/`: each breaks one rule (missing `world`, a replay-capable trace-only fixture, a non-sha256 prompt hash, expected failures without `expect_fail`, recorded request headers, a non-hex span id in the trace, and others).

The engine checks them in `engine/internal/capture/fixture_v1_schema_test.go`, which also validates a fresh capture against the schema; Python checks the schema fixtures and that `load_fixture` reads each valid fixture with its trace unchanged (`tests/test_fixture_v1.py`); TypeScript checks the schema fixtures and `loadFixture` (`tests/conformance/fixture-v1-conformance.test.ts`).

## Contract fixtures

`contracts/traces/` holds traces for the behavior-contract trace checks (`proto/attest/v1/protocol-spec.md`, Layer 3):

- `inc-1042-ts.trace.json`: the refund-desk INC-1042 TypeScript export converted by `attest-engine trace convert`. The Python export is `trace-v2/otlp/inc-1042-refund.trace.json`. In both, the second `issue_refund` retries the first after a payments timeout.
- `inc-1042-corrected.trace.json`: the Python trace with the retry replaced by a `lookup_order` that finds the refund, so `issue_refund` runs once for `O-1042`.

`engine/internal/assertion/trace_contracts_test.go` checks that `tool_cardinality` per `order_id` reports `issue_refund called 2× for order_id=O-1042 (max 1)` on both incident traces and passes on the corrected one.

`contracts/dsl.json` lists one case per behavior-contract DSL call: the Python call (`method`, `args`, `kwargs`), the TypeScript call (`method`, `args`), the spec both must emit, and the engine result (`status`, optionally the exact `explanation`) on each named trace. `traces` maps names to trace files under `protocol-tests/`; `contracts/traces/abab.trace.json` is a search/rephrase loop (A→B→A→B) ending in two failed tool calls. Python checks the emitted specs and the trace files against the trace v2 schema (`tests/test_contract_conformance.py`), TypeScript checks the emitted specs (`tests/conformance/contract-dsl-conformance.test.ts`), and the engine evaluates each spec (`engine/internal/assertion/trace_contracts_conformance_test.go`). The integration suites (`tests/integration/test_behavior_contracts.py`, `tests/integration/behavior-contracts.test.ts`) run the INC-1042 contract through a built engine.

`contracts/effect-dsl.json` does the same for the effect DSL (`world(snapshot)` then `count`, `unique` or `idempotent`), kept apart because the engine's conformance test evaluates every `dsl.json` case as a trace spec. `worlds` names the snapshots; a case's `world` picks one (or `null`), the spec both SDKs must emit has that snapshot as its `world`, and `result` is the engine's status (and optionally the exact explanation). Python and TypeScript check the emitted specs; the Python integration tests evaluate each case with the engine.

## Assertion id vectors

`ids/assertion-ids.json` holds the golden vectors for assertion id scheme 2 (see "Assertion ids (id scheme 2)" in `proto/attest/v1/protocol-spec.md`). Each vector gives `scope_id`, `assertion_type`, `spec_json` (raw JSON text), `occurrence_index` and the expected `canonical_spec`, `canonical_input` and `id`. The engine tests them in `engine/internal/report/assertionid`; every SDK must reproduce all of them byte for byte.
