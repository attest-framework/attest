"""OTelAdapter: OTLP/JSON encoding, the engine call contract and the export side.

The OTLP → trace mapping lives in the engine and is tested there
(engine/internal/trace). These tests cover what the Python adapter owns;
tests/integration/test_otel_engine.py runs the real engine.
"""

from __future__ import annotations

import json
import stat
import subprocess
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest

from attest import ENGINE_VERSION
from attest._proto.types import STEP_LLM_CALL, STEP_TOOL_CALL, Step, StepError, Trace
from attest.adapters.otel import (
    OTelAdapter,
    _any_value,
    _readable_spans_to_otlp,
    _span_dicts_to_otlp,
    to_otel_spans,
)
from attest.exceptions import OTLPConversionError
from attest.trace import TraceBuilder

# ---------------------------------------------------------------------------
# ReadableSpan stand-ins (opentelemetry-sdk is an optional extra)
# ---------------------------------------------------------------------------


@dataclass
class _Ctx:
    trace_id: int
    span_id: int


@dataclass
class _StatusCode:
    value: int


@dataclass
class _Status:
    status_code: _StatusCode = field(default_factory=lambda: _StatusCode(0))
    description: str | None = None


@dataclass
class _Span:
    name: str
    context: _Ctx | None
    parent: _Ctx | None = None
    start_time: int | None = 1_000
    end_time: int | None = 2_000
    attributes: dict[str, Any] = field(default_factory=dict)
    status: _Status = field(default_factory=_Status)


_TRACE_ID = 0x4BF92F3577B34DA6A3CE929D0E0E4736


class TestAnyValue:
    def test_any_value_scalars_and_collections_encode_as_otlp(self) -> None:
        assert _any_value(True) == {"boolValue": True}
        assert _any_value(42) == {"intValue": "42"}
        assert _any_value(1.5) == {"doubleValue": 1.5}
        assert _any_value("x") == {"stringValue": "x"}
        assert _any_value(b"hi") == {"bytesValue": "aGk="}
        assert _any_value(["a", 1]) == {
            "arrayValue": {"values": [{"stringValue": "a"}, {"intValue": "1"}]}
        }
        assert _any_value({"k": False}) == {
            "kvlistValue": {"values": [{"key": "k", "value": {"boolValue": False}}]}
        }

    @pytest.mark.parametrize("value", [float("nan"), float("inf")])
    def test_any_value_non_finite_float_raises_value_error(self, value: float) -> None:
        with pytest.raises(ValueError, match="finite"):
            _any_value(value)

    def test_any_value_unsupported_type_raises_type_error(self) -> None:
        with pytest.raises(TypeError, match="object"):
            _any_value(object())


class TestReadableSpanEncoding:
    def test_readable_spans_encode_ids_parent_times_status_and_attributes(self) -> None:
        root = _Span("invoke_agent a", _Ctx(_TRACE_ID, 1), attributes={"gen_ai.agent.name": "a"})
        child = _Span(
            "chat m",
            _Ctx(_TRACE_ID, 2),
            parent=_Ctx(_TRACE_ID, 1),
            start_time=1_100,
            end_time=1_900,
            attributes={"gen_ai.usage.input_tokens": 3},
            status=_Status(_StatusCode(2), "rate limited"),
        )

        doc = _readable_spans_to_otlp([root, child])  # type: ignore[list-item]

        spans = doc["resourceSpans"][0]["scopeSpans"][0]["spans"]
        assert spans[0] == {
            "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
            "spanId": "0000000000000001",
            "name": "invoke_agent a",
            "startTimeUnixNano": "1000",
            "endTimeUnixNano": "2000",
            "attributes": [{"key": "gen_ai.agent.name", "value": {"stringValue": "a"}}],
        }
        assert spans[1]["parentSpanId"] == "0000000000000001"
        assert spans[1]["status"] == {"code": 2, "message": "rate limited"}
        assert spans[1]["attributes"] == [
            {"key": "gen_ai.usage.input_tokens", "value": {"intValue": "3"}}
        ]

    def test_readable_span_without_context_raises_value_error(self) -> None:
        with pytest.raises(ValueError, match="no span context"):
            _readable_spans_to_otlp([_Span("x", None)])  # type: ignore[list-item]

    def test_from_spans_without_otel_installed_raises_import_error(self) -> None:
        with patch.dict("sys.modules", {"opentelemetry.sdk.trace": None}):
            with pytest.raises(ImportError, match="otel"):
                OTelAdapter.from_spans([])


class TestSpanDictEncoding:
    def test_span_dicts_encode_to_otlp_spans(self) -> None:
        doc = _span_dicts_to_otlp(
            [
                {
                    "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
                    "span_id": "0000000000000002",
                    "parent_span_id": "0000000000000001",
                    "name": "execute_tool t",
                    "start_time_unix_nano": 5,
                    "end_time_unix_nano": 9,
                    "attributes": {"gen_ai.tool.name": "t"},
                    "status": {"code": 2, "message": "boom"},
                }
            ]
        )

        span = doc["resourceSpans"][0]["scopeSpans"][0]["spans"][0]
        assert span == {
            "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
            "spanId": "0000000000000002",
            "parentSpanId": "0000000000000001",
            "name": "execute_tool t",
            "startTimeUnixNano": "5",
            "endTimeUnixNano": "9",
            "attributes": [{"key": "gen_ai.tool.name", "value": {"stringValue": "t"}}],
            "status": {"code": 2, "message": "boom"},
        }

    def test_span_dict_attributes_not_a_mapping_raises_value_error(self) -> None:
        with pytest.raises(ValueError, match="mapping"):
            _span_dicts_to_otlp([{"attributes": ["x"]}])


# ---------------------------------------------------------------------------
# Engine call contract (scripted engine; the real engine runs in integration)
# ---------------------------------------------------------------------------

_SCRIPT = """#!/bin/sh
case "$1" in
  version) echo "attest-engine {version}" ;;
  trace) printf '%s\\n' "$@" > "{dir}/args"; cat > "{dir}/stdin"; cat "{dir}/response"
         printf '%s' "{stderr}" >&2; exit {code} ;;
esac
"""


def _script_engine(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, response: str, code: int = 0, stderr: str = ""
) -> Path:
    (tmp_path / "response").write_text(response)
    engine = tmp_path / "attest-engine"
    engine.write_text(
        _SCRIPT.format(version=ENGINE_VERSION, dir=tmp_path, code=code, stderr=stderr)
    )
    engine.chmod(engine.stat().st_mode | stat.S_IXUSR)
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(engine))
    return tmp_path


class TestEngineCall:
    def test_from_otlp_mapping_sends_json_to_trace_convert_and_parses_trace(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        trace = {
            "schema_version": 2,
            "trace_id": "otel_4bf92f3577b34da6",
            "agent_id": "custom",
            "input": {},
            "output": {"message": "ok"},
            "steps": [
                {
                    "type": "tool_call",
                    "name": "t",
                    "args": {},
                    "result": {},
                    "tool_call_id": "c1",
                    "error": {"type": "timeout"},
                }
            ],
        }
        work = _script_engine(tmp_path, monkeypatch, json.dumps(trace))
        otlp = {"resourceSpans": []}

        result = OTelAdapter.from_otlp(otlp, agent_id="custom")

        assert (work / "args").read_text().split() == [
            "trace",
            "convert",
            "--agent-id",
            "custom",
            "-",
        ]
        assert json.loads((work / "stdin").read_text()) == otlp
        assert result.to_dict() == trace
        assert result.steps[0].error == StepError(type="timeout")

    def test_from_otlp_accepts_path_text_and_bytes(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        trace = {"schema_version": 2, "trace_id": "t", "output": {"message": ""}, "steps": []}
        work = _script_engine(tmp_path, monkeypatch, json.dumps(trace))
        path = tmp_path / "export.json"
        path.write_text('{"resourceSpans":[1]}')

        for source in (path, '{"resourceSpans":[1]}', b'{"resourceSpans":[1]}'):
            OTelAdapter.from_otlp(source)
            assert (work / "stdin").read_text() == '{"resourceSpans":[1]}'

    def test_from_otlp_engine_output_not_json_raises_otlp_conversion_error(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        _script_engine(tmp_path, monkeypatch, "not json")

        with pytest.raises(OTLPConversionError, match="not JSON"):
            OTelAdapter.from_otlp({"resourceSpans": []})

    def test_from_otlp_engine_timeout_raises_otlp_conversion_error(self) -> None:
        timeout = subprocess.TimeoutExpired(cmd="attest-engine", timeout=60)
        with (
            patch("attest.engine_manager.resolve_engine", return_value="attest-engine"),
            patch("subprocess.run", side_effect=timeout),
            pytest.raises(OTLPConversionError, match="timed out"),
        ):
            OTelAdapter.from_otlp({"resourceSpans": []})

    def test_from_otlp_engine_rejection_raises_otlp_conversion_error(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        _script_engine(
            tmp_path, monkeypatch, "", code=1, stderr="trace convert: traceId is not hex"
        )

        with pytest.raises(OTLPConversionError, match="traceId is not hex"):
            OTelAdapter.from_otlp({"resourceSpans": []})


# ---------------------------------------------------------------------------
# Export
# ---------------------------------------------------------------------------


def _spans_by_name(trace: Trace) -> dict[str, dict[str, Any]]:
    return {s["name"]: s for s in to_otel_spans(trace)}


class TestToOtelSpans:
    def test_export_roots_steps_under_an_invoke_agent_span(self) -> None:
        trace = (
            TraceBuilder(agent_id="desk")
            .add_llm_call("chat m", args={"model": "m"}, metadata={"duration_ms": 5})
            .add_tool_call("lookup", args={"id": 1}, metadata={"duration_ms": 3})
            .set_output(message="done")
            .build()
        )

        spans = to_otel_spans(trace)

        root = spans[0]
        assert root["name"] == "invoke_agent desk"
        assert root["parent_span_id"] is None
        assert root["attributes"] == {
            "gen_ai.operation.name": "invoke_agent",
            "gen_ai.agent.name": "desk",
        }
        assert [s["parent_span_id"] for s in spans[1:]] == [root["span_id"]] * 2
        assert [(s["start_time_unix_nano"], s["end_time_unix_nano"]) for s in spans] == [
            (0, 8_000_000),
            (0, 5_000_000),
            (5_000_000, 8_000_000),
        ]

    def test_export_tool_step_writes_current_semconv_attributes(self) -> None:
        trace = Trace(
            trace_id="trc",
            output={"message": ""},
            steps=[
                Step(
                    type=STEP_TOOL_CALL,
                    name="issue_refund",
                    args={"order_id": "O-1"},
                    result={"error": "timeout"},
                    tool_call_id="call_2",
                    error=StepError(type="payments_timeout", message="slow"),
                )
            ],
        )

        span = _spans_by_name(trace)["execute_tool issue_refund"]

        assert span["attributes"] == {
            "gen_ai.operation.name": "execute_tool",
            "gen_ai.tool.name": "issue_refund",
            "gen_ai.tool.call.id": "call_2",
            "gen_ai.tool.call.arguments": '{"order_id": "O-1"}',
            "gen_ai.tool.call.result": '{"error": "timeout"}',
            "error.type": "payments_timeout",
        }
        assert span["status"] == {"code": 2, "message": "slow"}

    def test_export_llm_step_writes_usage_cost_and_models(self) -> None:
        trace = Trace(
            trace_id="trc",
            output={"message": ""},
            steps=[
                Step(
                    type=STEP_LLM_CALL,
                    name="chat",
                    args={"model": "gpt-4.1", "provider": "openai", "operation": "chat"},
                    result={
                        "input_tokens": 3,
                        "output_tokens": 4,
                        "cost_usd": 0.01,
                        "model": "gpt-4.1-2025",
                        "finish_reasons": ["stop"],
                    },
                )
            ],
        )

        span = _spans_by_name(trace)["chat gpt-4.1"]

        assert span["attributes"] == {
            "gen_ai.operation.name": "chat",
            "gen_ai.request.model": "gpt-4.1",
            "gen_ai.provider.name": "openai",
            "gen_ai.usage.input_tokens": 3,
            "gen_ai.usage.output_tokens": 4,
            "gen_ai.usage.cost": 0.01,
            "gen_ai.response.model": "gpt-4.1-2025",
            "gen_ai.response.finish_reasons": ["stop"],
        }

    def test_export_tool_step_keeps_non_object_result_and_omits_absent_args(self) -> None:
        trace = Trace(
            trace_id="trc",
            output={"message": ""},
            steps=[Step(type=STEP_TOOL_CALL, name="t", result="plain")],  # type: ignore[arg-type]
        )

        attrs = _spans_by_name(trace)["execute_tool t"]["attributes"]

        assert attrs["gen_ai.tool.call.result"] == '"plain"'
        assert "gen_ai.tool.call.arguments" not in attrs

    def test_export_derives_trace_id_from_otel_prefix(self) -> None:
        trace = Trace(trace_id="otel_4bf92f3577b34da6", output={"message": ""})

        assert to_otel_spans(trace)[0]["trace_id"] == "4bf92f3577b34da60000000000000000"
