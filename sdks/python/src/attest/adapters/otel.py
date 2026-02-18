"""OpenTelemetry trace adapter for Attest."""

from __future__ import annotations

from collections.abc import Sequence
from typing import TYPE_CHECKING, Any

from attest._proto.types import Trace
from attest.trace import TraceBuilder

if TYPE_CHECKING:
    from opentelemetry.sdk.trace import ReadableSpan


def _require_otel() -> None:
    """Raise ImportError if opentelemetry-sdk is not installed."""
    try:
        import opentelemetry.sdk.trace  # noqa: F401
    except ImportError:
        raise ImportError("Install otel extras: uv add 'attest-ai[otel]'")


class OTelAdapter:
    """Maps OpenTelemetry spans to Attest traces using gen_ai.* semantic conventions.

    Span mapping:
    - Spans with gen_ai.operation.name == "chat" or kind == "gen_ai.completion" → llm_call steps
    - Spans with gen_ai.operation.name == "tool" or attribute "gen_ai.tool.name" → tool_call steps
    - All other spans are skipped.

    The root span (no parent, or outermost) determines the trace output and metadata.
    """

    def __init__(self, agent_id: str | None = None) -> None:
        self._agent_id = agent_id

    @classmethod
    def from_spans(
        cls,
        spans: Sequence[ReadableSpan],
        agent_id: str | None = None,
    ) -> Trace:
        """Build an Attest Trace from a sequence of OpenTelemetry ReadableSpans.

        Args:
            spans: Sequence of ReadableSpan objects from opentelemetry-sdk.
            agent_id: Optional agent identifier for the trace.

        Returns:
            Attest Trace populated from the spans.
        """
        _require_otel()

        adapter = cls(agent_id=agent_id)
        return adapter._build_trace(spans)

    def _build_trace(self, spans: Sequence[ReadableSpan]) -> Trace:
        """Internal trace builder from spans."""
        # Sort spans by start time
        sorted_spans = sorted(spans, key=lambda s: s.start_time or 0)

        builder = TraceBuilder(agent_id=self._agent_id)

        # Find root span: span with no parent or lowest start time
        root_span = self._find_root_span(sorted_spans)

        if root_span is not None:
            trace_id_hex = format(root_span.context.trace_id, "032x") if root_span.context else ""
            if trace_id_hex:
                builder.set_trace_id(f"otel_{trace_id_hex[:16]}")

        # Add steps from each span
        output_message = ""
        total_tokens: int | None = None
        cost_usd: float | None = None
        latency_ms: int | None = None
        model: str | None = None

        for span in sorted_spans:
            attrs = dict(span.attributes or {})
            step_type = self._classify_span(attrs, span.name)

            if step_type == "llm_call":
                step_args, step_result = self._extract_llm_step(attrs, span.name)
                builder.add_llm_call(
                    name=span.name,
                    args=step_args,
                    result=step_result,
                    metadata=self._span_metadata(span),
                )
                # Extract output message from last LLM call
                completion = attrs.get("gen_ai.completion", "")
                if completion:
                    output_message = str(completion)

                # Accumulate tokens
                input_tokens = attrs.get("gen_ai.usage.input_tokens", 0)
                output_tokens = attrs.get("gen_ai.usage.output_tokens", 0)
                span_tokens = int(input_tokens) + int(output_tokens)  # type: ignore[arg-type]
                if span_tokens > 0:
                    total_tokens = (total_tokens or 0) + span_tokens

                if model is None:
                    key = "gen_ai.response.model"
                    model = str(attrs[key]) if key in attrs else None
                    if model is None:
                        key = "gen_ai.request.model"
                        model = str(attrs[key]) if key in attrs else None

            elif step_type == "tool_call":
                step_args, step_result = self._extract_tool_step(attrs, span.name)
                tool_name = str(attrs.get("gen_ai.tool.name", span.name))
                builder.add_tool_call(
                    name=tool_name,
                    args=step_args,
                    result=step_result,
                    metadata=self._span_metadata(span),
                )

        # Compute latency from root span duration
        if (
            root_span is not None
            and root_span.start_time is not None
            and root_span.end_time is not None
        ):
            latency_ms = int((root_span.end_time - root_span.start_time) / 1_000_000)

        builder.set_output(message=output_message)
        builder.set_metadata(
            total_tokens=total_tokens,
            cost_usd=cost_usd,
            latency_ms=latency_ms,
            model=model,
        )

        return builder.build()

    def _find_root_span(self, spans: Sequence[ReadableSpan]) -> ReadableSpan | None:
        """Return the span with no valid parent, or the first span."""
        for span in spans:
            parent = span.parent
            if parent is None:
                return span
        return spans[0] if spans else None

    def _classify_span(self, attrs: dict[str, Any], name: str) -> str | None:
        """Return 'llm_call', 'tool_call', or None for unknown spans."""
        op = str(attrs.get("gen_ai.operation.name", ""))
        if op in ("chat", "completion", "generate_content") or "gen_ai.completion" in attrs:
            return "llm_call"
        if op == "tool" or "gen_ai.tool.name" in attrs:
            return "tool_call"
        # Fallback: check span name conventions
        if "completion" in name.lower() or "chat" in name.lower():
            return "llm_call"
        if "tool" in name.lower():
            return "tool_call"
        return None

    def _extract_llm_step(
        self, attrs: dict[str, Any], name: str
    ) -> tuple[dict[str, Any], dict[str, Any]]:
        """Extract args and result dicts from LLM span attributes."""
        args: dict[str, Any] = {}
        result: dict[str, Any] = {}

        if "gen_ai.request.model" in attrs:
            args["model"] = str(attrs["gen_ai.request.model"])
        if "gen_ai.system" in attrs:
            args["system"] = str(attrs["gen_ai.system"])
        if "gen_ai.prompt" in attrs:
            args["prompt"] = str(attrs["gen_ai.prompt"])

        if "gen_ai.completion" in attrs:
            result["completion"] = str(attrs["gen_ai.completion"])
        if "gen_ai.usage.input_tokens" in attrs:
            result["input_tokens"] = int(attrs["gen_ai.usage.input_tokens"])  # type: ignore[arg-type]
        if "gen_ai.usage.output_tokens" in attrs:
            result["output_tokens"] = int(attrs["gen_ai.usage.output_tokens"])  # type: ignore[arg-type]
        if "gen_ai.response.model" in attrs:
            result["model"] = str(attrs["gen_ai.response.model"])

        return args, result

    def _extract_tool_step(
        self, attrs: dict[str, Any], name: str
    ) -> tuple[dict[str, Any], dict[str, Any]]:
        """Extract args and result dicts from tool call span attributes."""
        args: dict[str, Any] = {}
        result: dict[str, Any] = {}

        if "gen_ai.tool.call.id" in attrs:
            args["call_id"] = str(attrs["gen_ai.tool.call.id"])
        if "gen_ai.tool.parameters" in attrs:
            args["parameters"] = attrs["gen_ai.tool.parameters"]
        if "gen_ai.tool.output" in attrs:
            result["output"] = attrs["gen_ai.tool.output"]

        return args, result

    def _span_metadata(self, span: ReadableSpan) -> dict[str, Any]:
        """Extract duration metadata from a span."""
        meta: dict[str, Any] = {}
        if span.start_time is not None and span.end_time is not None:
            meta["duration_ms"] = int((span.end_time - span.start_time) / 1_000_000)
        return meta
