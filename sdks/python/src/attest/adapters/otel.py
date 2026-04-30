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
    Step,
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
    if isinstance(value, bool) or not isinstance(value, int | float):
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

    @classmethod
    def from_otel_span_dicts(
        cls,
        span_dicts: Sequence[dict[str, Any]],
        agent_id: str | None = None,
    ) -> Trace:
        """Build an Attest Trace from OTLP-shaped span dicts.

        Inverse of :func:`to_otel_spans`. Accepts the dict shape produced by
        the export side (or any equivalent OTLP/JSON serialization) so
        consumers can verify round-trip fidelity without instantiating
        opentelemetry-sdk ReadableSpan objects.

        The dicts must carry ``attributes`` (mapping), ``name`` (str),
        ``start_time_unix_nano`` and ``end_time_unix_nano`` (ints), plus
        ``trace_id``/``span_id``/``parent_span_id`` for hierarchy. Other
        fields are ignored.

        Args:
            span_dicts: Sequence of OTLP-shaped span dicts.
            agent_id: Optional agent identifier for the trace.

        Returns:
            Attest Trace populated from the spans.
        """
        adapter = cls(agent_id=agent_id)
        wrapped = [_DictSpan(d) for d in span_dicts]
        return adapter._build_trace(wrapped)

    def _build_trace(self, spans: Sequence[Any]) -> Trace:
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

    def _find_root_span(self, spans: Sequence[Any]) -> Any | None:
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
            result["input_tokens"] = _require_numeric_attr(attrs, "gen_ai.usage.input_tokens")
        if "gen_ai.usage.output_tokens" in attrs:
            result["output_tokens"] = _require_numeric_attr(attrs, "gen_ai.usage.output_tokens")
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
    ) -> Step:
        """Construct an agent_call Step."""
        return Step(
            type=STEP_AGENT_CALL,
            name=name,
            args=args,
            result=result,
            metadata=metadata,
            agent_id=agent_id,
            agent_role=agent_role,
        )

    def _span_metadata(self, span: Any) -> dict[str, Any]:
        """Extract duration metadata from a span."""
        meta: dict[str, Any] = {}
        if span.start_time is not None and span.end_time is not None:
            meta["duration_ms"] = int((span.end_time - span.start_time) / 1_000_000)
        return meta


class _DictSpan:
    """Minimal ReadableSpan-shaped wrapper around an OTLP span dict.

    Exposes the attribute surface that ``OTelAdapter._build_trace`` reads
    from a real ``opentelemetry.sdk.trace.ReadableSpan``: ``name``,
    ``attributes``, ``start_time``, ``end_time``, ``parent``, ``context``.
    Used internally by :meth:`OTelAdapter.from_otel_span_dicts` so the
    round-trip path does not require opentelemetry-sdk to be importable.

    Required keys: ``name``, ``trace_id``, ``span_id``, plus
    ``start_time_unix_nano``, ``end_time_unix_nano``. Trace and span IDs
    must be hex strings (32-char and 16-char, but any hex length is
    accepted). Bad input raises ``ValueError`` with the offending key —
    no silent fallback to zero, since the dict source is normally our
    own ``to_otel_spans`` and a parse error means the data is malformed.
    """

    __slots__ = ("name", "attributes", "start_time", "end_time", "parent", "context")

    def __init__(self, data: dict[str, Any]) -> None:
        self.name: str = str(data.get("name", ""))
        attrs = data.get("attributes") or {}
        if not isinstance(attrs, dict):
            raise ValueError(
                f"OTLP span dict 'attributes' must be a mapping; got {type(attrs).__name__}"
            )
        self.attributes: dict[str, Any] = dict(attrs)
        self.start_time: int | None = data.get("start_time_unix_nano")
        self.end_time: int | None = data.get("end_time_unix_nano")

        parent_id = data.get("parent_span_id")
        self.parent = _DictSpanParent(parent_id) if parent_id else None

        trace_id_hex = data.get("trace_id")
        self.context = _DictSpanContext(_parse_hex_id(trace_id_hex, "trace_id"))


def _parse_hex_id(value: Any, field: str) -> int:
    """Parse a hex-encoded ID. Empty/None → 0. Invalid hex → ValueError.

    Distinguishes "absent" (acceptable, returns 0 for downstream
    ``format(..., "032x")``) from "present but malformed" (a producer
    bug worth surfacing).
    """
    if value is None or value == "":
        return 0
    if not isinstance(value, str):
        raise ValueError(
            f"OTLP span dict {field!r} must be a hex string; got {type(value).__name__}"
        )
    try:
        return int(value, 16)
    except ValueError as exc:
        raise ValueError(f"OTLP span dict {field!r} is not valid hex: {value!r}") from exc


class _DictSpanParent:
    __slots__ = ("span_id",)

    def __init__(self, span_id_hex: Any) -> None:
        self.span_id = _parse_hex_id(span_id_hex, "parent_span_id")


class _DictSpanContext:
    __slots__ = ("trace_id",)

    def __init__(self, trace_id: int) -> None:
        self.trace_id = trace_id


# Inverse of _OPERATION_TO_STEP, used by the export side. Picks the canonical
# operation name for each step type (e.g. STEP_LLM_CALL → "chat" by default).
# Producers may override with the original operation via Step.args["operation"].
_STEP_TO_OPERATION: dict[str, str] = {
    STEP_LLM_CALL: "chat",
    STEP_TOOL_CALL: "execute_tool",
    STEP_AGENT_CALL: "invoke_agent",
    STEP_RETRIEVAL: "retrieval",
}


def to_otel_spans(trace: Trace) -> list[dict[str, Any]]:
    """Export an Attest Trace as OTLP-compatible span dicts with gen_ai.* attrs.

    Returned spans are JSON-serializable dicts modeled on the OTLP/JSON span
    shape (``trace_id``, ``span_id``, ``parent_span_id``, ``name``,
    ``start_time_unix_nano``, ``end_time_unix_nano``, ``attributes``). They
    can be handed to an OTLP exporter, written to a file, or replayed back
    through ``OTelAdapter.from_spans`` for round-trip verification.

    The mapping inverts the ingest path:
      - llm_call    → ``gen_ai.operation.name = chat`` (or original op when
                      the trace was ingested with a different op such as
                      ``embeddings``, recorded in ``args["operation"]``)
      - tool_call   → ``gen_ai.operation.name = execute_tool``
      - agent_call  → ``gen_ai.operation.name = invoke_agent``
      - retrieval   → ``gen_ai.operation.name = retrieval``

    Step.agent_id / Step.agent_role round-trip via ``gen_ai.agent.id`` /
    ``gen_ai.agent.name``. Token usage and model identity round-trip via
    the standard ``gen_ai.usage.*`` and ``gen_ai.{request,response}.model``
    attribute families.

    What does NOT round-trip (by design — the contract is step-level):
      - ``Trace.input`` is not represented in OTLP spans.
      - ``Trace.metadata.latency_ms`` is recomputed from root-span duration
        on re-ingest, which equals the first step's ``duration_ms``. If
        the original Trace had a separately-set ``latency_ms``, it is lost.
      - Producers must keep ``args["operation"]`` consistent with
        ``step.type`` (e.g. ``embeddings`` is a valid override on an
        ``llm_call`` step, but ``execute_tool`` is not). A contradictory
        override produces an OTLP span that re-ingests with a different
        step type.

    Args:
        trace: Attest Trace to export.

    Returns:
        List of OTLP-compatible span dicts. Order matches Step order; each
        non-root span carries the root's span_id as ``parent_span_id``.
    """
    spans: list[dict[str, Any]] = []
    if not trace.steps:
        return spans

    trace_id_hex = _derive_trace_id_hex(trace.trace_id)
    root_span_id_hex = f"{1:016x}"
    cumulative_start_ns = 0

    for index, step in enumerate(trace.steps, start=1):
        attrs = _step_to_attributes(step)
        span_name = _step_to_span_name(step, attrs)

        duration_ns = 0
        meta = step.metadata or {}
        if isinstance(meta, dict):
            duration_ms = meta.get("duration_ms")
            if isinstance(duration_ms, int) and duration_ms > 0:
                duration_ns = duration_ms * 1_000_000

        start_ns = cumulative_start_ns
        end_ns = start_ns + duration_ns
        cumulative_start_ns = end_ns

        span: dict[str, Any] = {
            "trace_id": trace_id_hex,
            "span_id": f"{index:016x}",
            "parent_span_id": None if index == 1 else root_span_id_hex,
            "name": span_name,
            "kind": "INTERNAL",
            "start_time_unix_nano": start_ns,
            "end_time_unix_nano": end_ns,
            "attributes": attrs,
        }
        spans.append(span)

    return spans


def _derive_trace_id_hex(trace_id: str) -> str:
    """Return a 32-hex-char trace id derived from the Attest trace id.

    Attest trace ids ingested from OTel start with ``otel_<16hex>``; pad
    them out so OTLP consumers see a valid 128-bit hex id. Other trace
    ids are hashed deterministically into 32 hex chars.
    """
    if trace_id.startswith("otel_"):
        suffix = trace_id[len("otel_") :]
        clean = "".join(c for c in suffix if c in "0123456789abcdef")
        if len(clean) >= 16:
            return (clean + "0" * 32)[:32]
    import hashlib

    return hashlib.sha256(trace_id.encode("utf-8")).hexdigest()[:32]


def _step_to_attributes(step: Any) -> dict[str, Any]:
    """Build the gen_ai.* attribute dict for a single Step."""
    attrs: dict[str, Any] = {}

    args = step.args or {}
    result = step.result or {}

    operation = args.get("operation") if isinstance(args, dict) else None
    if not operation:
        operation = _STEP_TO_OPERATION.get(step.type, "chat")
    attrs["gen_ai.operation.name"] = operation

    if step.agent_id is not None:
        attrs["gen_ai.agent.id"] = step.agent_id
    if step.agent_role is not None:
        attrs["gen_ai.agent.name"] = step.agent_role

    if step.type == STEP_LLM_CALL:
        if isinstance(args, dict):
            if "model" in args:
                attrs["gen_ai.request.model"] = args["model"]
            if "system" in args:
                attrs["gen_ai.system"] = args["system"]
            if "prompt" in args:
                attrs["gen_ai.prompt"] = args["prompt"]
        if isinstance(result, dict):
            if "completion" in result:
                attrs["gen_ai.completion"] = result["completion"]
            if "input_tokens" in result:
                attrs["gen_ai.usage.input_tokens"] = result["input_tokens"]
            if "output_tokens" in result:
                attrs["gen_ai.usage.output_tokens"] = result["output_tokens"]
            if "model" in result:
                attrs["gen_ai.response.model"] = result["model"]

    elif step.type == STEP_TOOL_CALL:
        attrs["gen_ai.tool.name"] = step.name
        if isinstance(args, dict):
            if "call_id" in args:
                attrs["gen_ai.tool.call.id"] = args["call_id"]
            if "parameters" in args:
                attrs["gen_ai.tool.parameters"] = args["parameters"]
        if isinstance(result, dict) and "output" in result:
            attrs["gen_ai.tool.output"] = result["output"]

    elif step.type == STEP_AGENT_CALL:
        if isinstance(args, dict):
            if "agent_id" in args:
                attrs["gen_ai.agent.id"] = args["agent_id"]
            if "agent_name" in args:
                attrs["gen_ai.agent.name"] = args["agent_name"]
            if "description" in args:
                attrs["gen_ai.agent.description"] = args["description"]
            if "prompt" in args:
                attrs["gen_ai.prompt"] = args["prompt"]
        if isinstance(result, dict) and "completion" in result:
            attrs["gen_ai.completion"] = result["completion"]

    elif step.type == STEP_RETRIEVAL:
        if isinstance(args, dict):
            if "query" in args:
                attrs["gen_ai.retrieval.query"] = args["query"]
            if "source" in args:
                attrs["gen_ai.retrieval.source"] = args["source"]
        if isinstance(result, dict):
            if "documents_count" in result:
                attrs["gen_ai.retrieval.documents.count"] = result["documents_count"]
            if "documents" in result:
                attrs["gen_ai.retrieval.documents"] = result["documents"]

    return attrs


def _step_to_span_name(step: Any, attrs: dict[str, Any]) -> str:
    """Pick a span name following OTel GenAI legacy ``<op> <model>`` convention."""
    op = attrs.get("gen_ai.operation.name", step.type)
    if step.type == STEP_LLM_CALL:
        model = attrs.get("gen_ai.request.model") or attrs.get("gen_ai.response.model")
        if model:
            return f"{op} {model}"
    if step.type == STEP_TOOL_CALL:
        return f"{op} {step.name}"
    if step.type == STEP_AGENT_CALL:
        agent_name = attrs.get("gen_ai.agent.name") or step.name
        return f"{op} {agent_name}"
    return str(op)
