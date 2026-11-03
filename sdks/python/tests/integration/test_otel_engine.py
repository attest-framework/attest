"""OTelAdapter against the engine built by ``make engine``.

The OTLP → trace mapping runs in ``attest-engine trace convert``; these tests
check that the Python adapter returns exactly the golden traces in
protocol-tests/trace-v2/otlp (the TypeScript adapter asserts the same files),
that exported spans convert back, and that malformed input is an error.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest

from attest._proto.types import STATUS_NOT_EVALUATED, STEP_LLM_CALL, STEP_TOOL_CALL
from attest.adapters.otel import OTelAdapter, to_otel_spans
from attest.exceptions import OTLPConversionError
from attest.expect import expect
from attest.plugin import AttestEngineFixture
from attest.trace import TraceBuilder
from tests.integration.conftest import _engine_binary_path

_FIXTURES = Path(__file__).resolve().parents[4] / "protocol-tests" / "trace-v2"
_GOLDEN = sorted((_FIXTURES / "otlp").glob("*.otlp.json"))
_MALFORMED = sorted((_FIXTURES / "otlp-invalid").glob("*.json"))


@pytest.fixture(autouse=True)
def engine_path(monkeypatch: pytest.MonkeyPatch) -> str:
    path = _engine_binary_path()
    if not Path(path).exists():
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    monkeypatch.setenv("ATTEST_ENGINE_PATH", path)
    return path


def _golden(otlp_path: Path) -> Any:
    return json.loads(otlp_path.with_name(otlp_path.name.replace(".otlp.", ".trace.")).read_text())


@pytest.mark.parametrize("path", _GOLDEN, ids=lambda p: p.name)
def test_from_otlp_returns_golden_trace(path: Path) -> None:
    trace = OTelAdapter.from_otlp(path)

    assert trace.to_dict() == _golden(path)


@pytest.mark.parametrize("path", _GOLDEN, ids=lambda p: p.name)
def test_from_otlp_mapping_and_text_match_file(path: Path) -> None:
    text = path.read_text()

    assert OTelAdapter.from_otlp(json.loads(text)).to_dict() == _golden(path)
    assert OTelAdapter.from_otlp(text).to_dict() == _golden(path)


@pytest.mark.parametrize("path", _MALFORMED, ids=lambda p: p.name)
def test_from_otlp_malformed_input_raises_otlp_conversion_error(path: Path) -> None:
    with pytest.raises(OTLPConversionError):
        OTelAdapter.from_otlp(path.read_bytes())


def test_to_otel_spans_round_trips_through_engine() -> None:
    original = (
        TraceBuilder(agent_id="refund-desk")
        .add_llm_call(
            "chat m",
            args={"model": "m"},
            result={"input_tokens": 5, "output_tokens": 2, "completion": "calling"},
            metadata={"duration_ms": 4},
        )
        .add_tool_call(
            "issue_refund",
            args={"order_id": "O-1"},
            result={"ok": True},
            metadata={"duration_ms": 3},
        )
        .set_output(message="calling")
        .build()
    )

    trace = OTelAdapter.from_otel_span_dicts(to_otel_spans(original))

    assert trace.agent_id == "refund-desk"
    assert [(s.type, s.name, s.args, s.result) for s in trace.steps] == [
        (
            STEP_LLM_CALL,
            "chat m",
            {"model": "m", "operation": "chat"},
            {"input_tokens": 5, "output_tokens": 2, "completion": "calling"},
        ),
        (STEP_TOOL_CALL, "issue_refund", {"order_id": "O-1"}, {"ok": True}),
    ]
    assert trace.metadata is not None
    assert trace.metadata.total_tokens == 7
    assert trace.output == {"message": "calling"}
    assert len(trace.turns) == 1


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
    context: _Ctx
    parent: _Ctx | None
    start_time: int
    end_time: int
    attributes: dict[str, Any]
    status: _Status = field(default_factory=_Status)


def test_from_spans_marks_repeated_failed_tool_call_as_retry() -> None:
    tid = 0x29478F3E0A07FF5725FAF3112A8E3663
    root = _Ctx(tid, 1)
    refund = {
        "gen_ai.operation.name": "execute_tool",
        "gen_ai.tool.name": "issue_refund",
        "gen_ai.tool.call.arguments": '{"order_id":"O-1042"}',
    }
    spans = [
        _Span("invoke_agent desk", root, None, 0, 100, {"gen_ai.operation.name": "invoke_agent"}),
        _Span(
            "execute_tool issue_refund",
            _Ctx(tid, 2),
            root,
            10,
            20,
            refund,
            _Status(_StatusCode(2), "timeout"),
        ),
        _Span("execute_tool issue_refund", _Ctx(tid, 3), root, 30, 40, refund),
    ]

    with patch("attest.adapters.otel._require_otel"):
        trace = OTelAdapter.from_spans(spans, agent_id="desk")  # type: ignore[arg-type]

    assert trace.trace_id == "otel_29478f3e0a07ff57"
    assert trace.agent_id == "desk"
    assert trace.steps[0].error is not None and trace.steps[0].error.message == "timeout"
    assert trace.steps[1].retry_of == 0


def test_cost_under_without_cost_evidence_is_not_evaluated(engine: AttestEngineFixture) -> None:
    trace = OTelAdapter.from_otlp(_FIXTURES / "otlp" / "inc-1042-refund.otlp.json")

    result = engine.inspect(expect(trace).cost_under(0.05))

    (r,) = result.assertion_results
    assert (r.status, r.reason, r.explanation) == (
        STATUS_NOT_EVALUATED,
        "missing_evidence",
        "metadata.cost_usd absent",
    )
