"""Tests for the OTelAdapter."""

from __future__ import annotations

from collections.abc import Generator
from contextlib import contextmanager
from unittest.mock import MagicMock, patch

import pytest

from attest._proto.types import STEP_LLM_CALL, STEP_TOOL_CALL
from attest.adapters.otel import OTelAdapter


def _make_span(
    name: str,
    attrs: dict[str, object],
    *,
    trace_id: int = 0xDEADBEEF12345678DEADBEEF12345678,
    span_id: int = 0x1234567890ABCDEF,
    parent_span_id: int | None = None,
    start_time: int = 1_000_000_000,
    end_time: int = 2_000_000_000,
) -> MagicMock:
    """Build a minimal mock ReadableSpan."""
    span = MagicMock()
    span.name = name
    span.attributes = attrs
    span.start_time = start_time
    span.end_time = end_time

    ctx = MagicMock()
    ctx.trace_id = trace_id
    ctx.span_id = span_id
    span.context = ctx

    if parent_span_id is None:
        span.parent = None
    else:
        parent = MagicMock()
        parent.span_id = parent_span_id
        span.parent = parent

    return span


@contextmanager
def _otel_available() -> Generator[None, None, None]:
    """Patch _require_otel to be a no-op (simulates otel being installed)."""
    with patch("attest.adapters.otel._require_otel"):
        yield


class TestOTelAdapterImportGuard:
    """Verify ImportError when opentelemetry is not installed."""

    def test_raises_import_error_when_otel_missing(self) -> None:
        # _require_otel is NOT patched here — it will raise ImportError
        # because opentelemetry-sdk is not installed in the test environment.
        with pytest.raises(ImportError, match="Install otel extras"):
            OTelAdapter.from_spans([])  # type: ignore[arg-type]


class TestOTelAdapterFromSpans:
    """Tests for OTelAdapter.from_spans() with mocked spans.

    All tests patch _require_otel to bypass the opentelemetry install check.
    """

    def test_empty_spans_returns_trace(self) -> None:
        with _otel_available():
            trace = OTelAdapter.from_spans([])
        assert trace is not None
        assert trace.steps == []

    def test_llm_call_span_becomes_llm_step(self) -> None:
        span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.request.model": "gpt-4.1",
                "gen_ai.completion": "Hello world",
                "gen_ai.usage.input_tokens": 10,
                "gen_ai.usage.output_tokens": 5,
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert len(trace.steps) == 1
        step = trace.steps[0]
        assert step.type == STEP_LLM_CALL
        assert step.args is not None
        assert step.args.get("model") == "gpt-4.1"
        assert step.result is not None
        assert step.result.get("completion") == "Hello world"

    def test_tool_call_span_becomes_tool_step(self) -> None:
        span = _make_span(
            "tool_call",
            {
                "gen_ai.tool.name": "search_web",
                "gen_ai.tool.parameters": '{"query": "Paris"}',
                "gen_ai.tool.output": '{"results": []}',
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert len(trace.steps) == 1
        step = trace.steps[0]
        assert step.type == STEP_TOOL_CALL
        assert step.name == "search_web"

    def test_output_message_from_last_llm_completion(self) -> None:
        span = _make_span(
            "completion",
            {
                "gen_ai.operation.name": "completion",
                "gen_ai.completion": "Paris is the answer.",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.output.get("message") == "Paris is the answer."

    def test_latency_computed_from_root_span(self) -> None:
        # 1 second = 1_000_000_000 ns → 1000 ms
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "hi"},
            start_time=0,
            end_time=1_000_000_000,
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.metadata is not None
        assert trace.metadata.latency_ms == 1000

    def test_token_accumulation(self) -> None:
        span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.completion": "ok",
                "gen_ai.usage.input_tokens": 50,
                "gen_ai.usage.output_tokens": 25,
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 75

    def test_model_extracted_from_response_model(self) -> None:
        span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.response.model": "gpt-4.1-mini",
                "gen_ai.completion": "response",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.metadata is not None
        assert trace.metadata.model == "gpt-4.1-mini"

    def test_model_falls_back_to_request_model(self) -> None:
        span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.request.model": "gpt-4.1",
                "gen_ai.completion": "response",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.metadata is not None
        assert trace.metadata.model == "gpt-4.1"

    def test_trace_id_derived_from_otel_trace_id(self) -> None:
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "ok"},
            trace_id=0xAABBCCDD11223344AABBCCDD11223344,
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.trace_id.startswith("otel_")

    def test_multiple_spans_multiple_steps(self) -> None:
        llm_span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.completion": "I'll search for that.",
            },
            start_time=0,
            end_time=500_000_000,
        )
        tool_span = _make_span(
            "tool_call",
            {
                "gen_ai.tool.name": "search",
                "gen_ai.tool.output": "results",
            },
            start_time=500_000_000,
            end_time=800_000_000,
            parent_span_id=0x1234567890ABCDEF,
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([llm_span, tool_span])
        assert len(trace.steps) == 2
        assert trace.steps[0].type == STEP_LLM_CALL
        assert trace.steps[1].type == STEP_TOOL_CALL

    def test_unknown_span_skipped(self) -> None:
        span = _make_span("http.request", {"http.method": "GET", "http.url": "https://example.com"})
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps == []

    def test_agent_id_set_on_trace(self) -> None:
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "ok"},
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span], agent_id="my-agent")
        assert trace.agent_id == "my-agent"

    def test_step_duration_metadata(self) -> None:
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "hi"},
            start_time=0,
            end_time=200_000_000,  # 200ms
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps[0].metadata is not None
        assert trace.steps[0].metadata.get("duration_ms") == 200

    def test_tool_name_from_attribute(self) -> None:
        span = _make_span(
            "some_span_name",
            {"gen_ai.tool.name": "my_tool"},
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps[0].name == "my_tool"

    def test_from_spans_with_agent_id(self) -> None:
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "response"},
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span], agent_id="inst-agent")
        assert trace.agent_id == "inst-agent"
