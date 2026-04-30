"""OpenTelemetry trace adapter for Attest.

Implements the ingest side of OpenTelemetry GenAI semantic conventions:
maps spans tagged with `gen_ai.operation.name` (and related attributes) into
Attest steps. The classifier uses an attribute-driven dispatch table; the
span name is consulted only when the operation attribute is absent.

Supported operations (per OTel GenAI semantic conventions):

    chat, text_completion, completion, generate_content  → llm_call
    embeddings                                            → llm_call
    invoke_agent                                          → agent_call
    execute_tool, tool                                    → tool_call
    retrieval (and `gen_ai.retrieval.*` attribute family) → retrieval

Agent hierarchy attributes `gen_ai.agent.name` and `gen_ai.agent.id` are
mapped onto Step.agent_role / Step.agent_id so TraceTree resolution sees
the same parent/child structure that producers emit.
"""

from __future__ import annotations

from collections.abc import Sequence
from typing import TYPE_CHECKING, Any

from attest._proto.types import (
    STEP_AGENT_CALL,
    STEP_LLM_CALL,
    STEP_RETRIEVAL,
    STEP_TOOL_CALL,
    Trace,
)
from attest.adapters._base import BaseAdapter

if TYPE_CHECKING:
    from opentelemetry.sdk.trace import ReadableSpan


# OTel GenAI operation.name → Attest step type. Authoritative when the
# attribute is present; the span name is only consulted as fallback.
_OPERATION_TO_STEP: dict[str, str] = {
    # LLM operations
    "chat": STEP_LLM_CALL,
    "completion": STEP_LLM_CALL,
    "text_completion": STEP_LLM_CALL,
    "generate_content": STEP_LLM_CALL,
    "embeddings": STEP_LLM_CALL,
    # Tool operations
    "execute_tool": STEP_TOOL_CALL,
    "tool": STEP_TOOL_CALL,
    # Agent operations
    "invoke_agent": STEP_AGENT_CALL,
    "create_agent": STEP_AGENT_CALL,
    # Retrieval (GenAI conventions reserve a retrieval span family)
    "retrieval": STEP_RETRIEVAL,
    "retrieve": STEP_RETRIEVAL,
}


def _require_otel() -> None:
    """Raise ImportError if opentelemetry-sdk is not installed."""
    try:
        import opentelemetry.sdk.trace  # noqa: F401
    except ImportError:
        raise ImportError("Install otel extras: uv add 'attest-ai[otel]'")


def _require_numeric_attr(attrs: dict[str, Any], key: str) -> int:
    """Return ``int(attrs[key])`` when present, ``0`` when missing.

    Raises ``TypeError`` if the attribute is set to a non-numeric value such
    as a sequence — the OTel GenAI semantic conventions type usage counters
    as scalar ints, so anything else is a producer bug that should surface
    rather than be silently coerced.
    """
    value = attrs.get(key)
    if value is None:
        return 0
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise TypeError(
            f"OTel attribute {key!r} must be numeric per gen_ai semantic "
            f"conventions; got {type(value).__name__}"
        )
    return int(value)


class OTelAdapter(BaseAdapter):
    """Maps OpenTelemetry spans to Attest traces using gen_ai.* semantic conventions.

    Span classification is attribute-driven: `gen_ai.operation.name` is the
    primary signal, with span name falling back only when no operation
    attribute is set. Agent hierarchy attributes (`gen_ai.agent.name`,
    `gen_ai.agent.id`) propagate to Step.agent_role and Step.agent_id so
    downstream TraceTree consumers see the same parent/child structure.
    """

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
        sorted_spans = sorted(spans, key=lambda s: s.start_time or 0)

        builder = self._create_builder()
        root_span = self._find_root_span(sorted_spans)

        if root_span is not None:
            trace_id_hex = format(root_span.context.trace_id, "032x") if root_span.context else ""
            if trace_id_hex:
                builder.set_trace_id(f"otel_{trace_id_hex[:16]}")

        output_message = ""
        total_tokens: int | None = None
        cost_usd: float | None = None
        latency_ms: int | None = None
        model: str | None = None

        for span in sorted_spans:
            attrs = dict(span.attributes or {})
            step_type = self._classify_span(attrs, span.name)
            if step_type is None:
                continue

            agent_id, agent_role = self._extract_agent_identity(attrs)

            if step_type == STEP_LLM_CALL:
                step_args, step_result = self._extract_llm_step(attrs)
                builder.add_llm_call(
                    name=span.name,
                    args=step_args,
                    result=step_result,
                    metadata=self._span_metadata(span),
                    agent_id=agent_id,
                    agent_role=agent_role,
                )
                completion = attrs.get("gen_ai.completion", "")
                if completion:
                    output_message = str(completion)

                input_tokens = _require_numeric_attr(attrs, "gen_ai.usage.input_tokens")
                output_tokens = _require_numeric_attr(attrs, "gen_ai.usage.output_tokens")
                span_tokens = input_tokens + output_tokens
                if span_tokens > 0:
                    total_tokens = (total_tokens or 0) + span_tokens

                if model is None:
                    model = self._extract_model(attrs)

            elif step_type == STEP_TOOL_CALL:
                step_args, step_result = self._extract_tool_step(attrs)
                tool_name = str(attrs.get("gen_ai.tool.name", span.name))
                builder.add_tool_call(
                    name=tool_name,
                    args=step_args,
                    result=step_result,
                    metadata=self._span_metadata(span),
                    agent_id=agent_id,
                    agent_role=agent_role,
                )

            elif step_type == STEP_AGENT_CALL:
                step_args, step_result = self._extract_agent_step(attrs)
                builder.add_step(
                    self._build_agent_step(
                        name=str(attrs.get("gen_ai.agent.name", span.name)),
                        args=step_args,
                        result=step_result,
                        metadata=self._span_metadata(span),
                        agent_id=agent_id,
                        agent_role=agent_role,
                    )
                )

            elif step_type == STEP_RETRIEVAL:
                step_args, step_result = self._extract_retrieval_step(attrs)
                builder.add_retrieval(
                    name=span.name,
                    args=step_args,
                    result=step_result,
                    metadata=self._span_metadata(span),
                    agent_id=agent_id,
                    agent_role=agent_role,
                )

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
            if span.parent is None:
                return span
        return spans[0] if spans else None

    def _classify_span(self, attrs: dict[str, Any], name: str) -> str | None:
        """Classify a span as llm_call/tool_call/agent_call/retrieval, or None.

        Dispatch order:
        1. `gen_ai.operation.name` attribute (authoritative per spec).
        2. Presence of operation-specific attributes (`gen_ai.tool.name`,
           `gen_ai.agent.name`, `gen_ai.completion`, retrieval families).
        3. Span name fallback for producers that emit only the legacy span
           name (`chat <model>`, `completion <model>`, etc.) without the
           operation attribute. This branch is intentionally narrow: it
           accepts only the canonical operation tokens, not any substring.
        """
        op = str(attrs.get("gen_ai.operation.name", "")).strip().lower()
        if op:
            mapped = _OPERATION_TO_STEP.get(op)
            if mapped is not None:
                return mapped

        if "gen_ai.tool.name" in attrs:
            return STEP_TOOL_CALL
        if "gen_ai.agent.name" in attrs or "gen_ai.agent.id" in attrs:
            return STEP_AGENT_CALL
        if any(k.startswith("gen_ai.retrieval.") for k in attrs):
            return STEP_RETRIEVAL
        if "gen_ai.completion" in attrs or "gen_ai.prompt" in attrs:
            return STEP_LLM_CALL

        first_token = name.strip().split(None, 1)[0].lower() if name else ""
        if first_token in _OPERATION_TO_STEP:
            return _OPERATION_TO_STEP[first_token]
        return None

    def _extract_agent_identity(self, attrs: dict[str, Any]) -> tuple[str | None, str | None]:
        """Return (agent_id, agent_role) from gen_ai.agent.* attributes."""
        agent_id_raw = attrs.get("gen_ai.agent.id")
        agent_name_raw = attrs.get("gen_ai.agent.name")
        agent_id = str(agent_id_raw) if agent_id_raw is not None else None
        agent_role = str(agent_name_raw) if agent_name_raw is not None else None
        return agent_id, agent_role

    def _extract_model(self, attrs: dict[str, Any]) -> str | None:
        """Resolve model id, preferring response.model over request.model."""
        if "gen_ai.response.model" in attrs:
            return str(attrs["gen_ai.response.model"])
        if "gen_ai.request.model" in attrs:
            return str(attrs["gen_ai.request.model"])
        return None

    def _extract_llm_step(self, attrs: dict[str, Any]) -> tuple[dict[str, Any], dict[str, Any]]:
        """Extract args and result dicts from LLM span attributes."""
        args: dict[str, Any] = {}
        result: dict[str, Any] = {}

        if "gen_ai.request.model" in attrs:
            args["model"] = str(attrs["gen_ai.request.model"])
        if "gen_ai.system" in attrs:
            args["system"] = str(attrs["gen_ai.system"])
        if "gen_ai.prompt" in attrs:
            args["prompt"] = str(attrs["gen_ai.prompt"])
        op = str(attrs.get("gen_ai.operation.name", "")).strip().lower()
        if op:
            args["operation"] = op

        if "gen_ai.completion" in attrs:
            result["completion"] = str(attrs["gen_ai.completion"])
        if "gen_ai.usage.input_tokens" in attrs:
            result["input_tokens"] = int(attrs["gen_ai.usage.input_tokens"])
        if "gen_ai.usage.output_tokens" in attrs:
            result["output_tokens"] = int(attrs["gen_ai.usage.output_tokens"])
        if "gen_ai.response.model" in attrs:
            result["model"] = str(attrs["gen_ai.response.model"])

        return args, result

    def _extract_tool_step(self, attrs: dict[str, Any]) -> tuple[dict[str, Any], dict[str, Any]]:
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

    def _extract_agent_step(self, attrs: dict[str, Any]) -> tuple[dict[str, Any], dict[str, Any]]:
        """Extract args and result dicts from invoke_agent span attributes."""
        args: dict[str, Any] = {}
        result: dict[str, Any] = {}

        if "gen_ai.agent.id" in attrs:
            args["agent_id"] = str(attrs["gen_ai.agent.id"])
        if "gen_ai.agent.name" in attrs:
            args["agent_name"] = str(attrs["gen_ai.agent.name"])
        if "gen_ai.agent.description" in attrs:
            args["description"] = str(attrs["gen_ai.agent.description"])
        if "gen_ai.prompt" in attrs:
            args["prompt"] = str(attrs["gen_ai.prompt"])

        if "gen_ai.completion" in attrs:
            result["completion"] = str(attrs["gen_ai.completion"])
        return args, result

    def _extract_retrieval_step(
        self, attrs: dict[str, Any]
    ) -> tuple[dict[str, Any], dict[str, Any]]:
        """Extract args and result dicts from retrieval span attributes."""
        args: dict[str, Any] = {}
        result: dict[str, Any] = {}

        if "gen_ai.retrieval.query" in attrs:
            args["query"] = str(attrs["gen_ai.retrieval.query"])
        if "gen_ai.retrieval.source" in attrs:
            args["source"] = str(attrs["gen_ai.retrieval.source"])
        if "gen_ai.retrieval.documents.count" in attrs:
            result["documents_count"] = int(attrs["gen_ai.retrieval.documents.count"])
        if "gen_ai.retrieval.documents" in attrs:
            result["documents"] = attrs["gen_ai.retrieval.documents"]
        return args, result

    def _build_agent_step(
        self,
        *,
        name: str,
        args: dict[str, Any],
        result: dict[str, Any],
        metadata: dict[str, Any],
        agent_id: str | None,
        agent_role: str | None,
    ) -> Any:
        """Construct an agent_call Step. Imported lazily to avoid circular import."""
        from attest._proto.types import Step

        return Step(
            type=STEP_AGENT_CALL,
            name=name,
            args=args,
            result=result,
            metadata=metadata,
            agent_id=agent_id,
            agent_role=agent_role,
        )

    def _span_metadata(self, span: ReadableSpan) -> dict[str, Any]:
        """Extract duration metadata from a span."""
        meta: dict[str, Any] = {}
        if span.start_time is not None and span.end_time is not None:
            meta["duration_ms"] = int((span.end_time - span.start_time) / 1_000_000)
        return meta
