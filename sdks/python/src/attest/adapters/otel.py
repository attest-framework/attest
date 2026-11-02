"""OpenTelemetry trace adapter for Attest.

OTLP → Attest trace conversion runs in the engine (``attest-engine trace
convert``), the single implementation of the OpenTelemetry GenAI semantic
convention mapping shared by every SDK. This module only encodes spans as
OTLP/JSON, calls the engine and parses the trace v2 it returns; it holds no
mapping of its own. See ``docs/guides/trace-check.md`` for the mapping.

``to_otel_spans`` is the export side: it writes an Attest trace as OTLP-shaped
span dicts with ``gen_ai.*`` attributes that the engine converts back.
"""

from __future__ import annotations

import base64
import hashlib
import json
import math
import os
import subprocess
from collections.abc import Mapping, Sequence
from typing import TYPE_CHECKING, Any

from attest._proto.types import (
    STEP_AGENT_CALL,
    STEP_LLM_CALL,
    STEP_RETRIEVAL,
    STEP_TOOL_CALL,
    Trace,
)
from attest.adapters._base import BaseAdapter
from attest.exceptions import OTLPConversionError

if TYPE_CHECKING:
    from opentelemetry.sdk.trace import ReadableSpan

# Upper bound for one `attest-engine trace convert` run.
_CONVERT_TIMEOUT_S = 60

# OTLP status code for an error span.
_STATUS_CODE_ERROR = 2


def _require_otel() -> None:
    """Raise ImportError if opentelemetry-sdk is not installed."""
    try:
        import opentelemetry.sdk.trace  # noqa: F401
    except ImportError:
        raise ImportError("Install otel extras: uv add 'attest-ai[otel]'")


class OTelAdapter(BaseAdapter):
    """Builds Attest traces from OpenTelemetry spans through the engine conversion."""

    @classmethod
    def from_spans(
        cls,
        spans: Sequence[ReadableSpan],
        agent_id: str | None = None,
    ) -> Trace:
        """Build an Attest Trace from opentelemetry-sdk ReadableSpans of one trace.

        Args:
            spans: Finished spans, for example from an ``InMemorySpanExporter``.
            agent_id: Trace agent_id; defaults to the first turn's ``gen_ai.agent.name``.

        Raises:
            OTLPConversionError: The engine rejected the spans, timed out or printed no trace.
            FileNotFoundError: No engine was found and download is disabled.
            EngineVersionMismatchError: The engine found is not this SDK's version.
        """
        _require_otel()
        return cls.from_otlp(_readable_spans_to_otlp(spans), agent_id=agent_id)

    @classmethod
    def from_otel_span_dicts(
        cls,
        span_dicts: Sequence[Mapping[str, Any]],
        agent_id: str | None = None,
    ) -> Trace:
        """Build an Attest Trace from span dicts in the shape :func:`to_otel_spans` writes.

        Each dict carries ``trace_id`` and ``span_id`` (hex), optional
        ``parent_span_id``, ``name``, ``start_time_unix_nano``,
        ``end_time_unix_nano``, ``attributes`` (mapping) and optional
        ``status`` (``{"code": 2, "message": ...}`` for errors).

        Raises:
            OTLPConversionError: The engine rejected the spans, timed out or printed no trace.
            FileNotFoundError: No engine was found and download is disabled.
            EngineVersionMismatchError: The engine found is not this SDK's version.
        """
        return cls.from_otlp(_span_dicts_to_otlp(span_dicts), agent_id=agent_id)

    @classmethod
    def from_otlp(
        cls,
        otlp: Mapping[str, Any] | str | bytes | os.PathLike[str],
        agent_id: str | None = None,
    ) -> Trace:
        """Convert an OTLP/JSON export (``resourceSpans`` → ``scopeSpans`` → ``spans``).

        Args:
            otlp: The export as a parsed mapping, JSON text (``str``) or bytes, or a file
                path as a ``pathlib.Path`` (a ``str`` is always JSON text).
            agent_id: Trace agent_id; defaults to the first turn's ``gen_ai.agent.name``.

        Raises:
            OTLPConversionError: The engine rejected the export (malformed JSON, invalid
                ids or timestamps, more than one trace, non-numeric token counts, ...),
                timed out, or printed no trace.
            FileNotFoundError: No engine was found and download is disabled.
            EngineVersionMismatchError: The engine found is not this SDK's version.
        """
        if isinstance(otlp, Mapping):
            payload = json.dumps(otlp, allow_nan=False).encode("utf-8")
        elif isinstance(otlp, bytes):
            payload = otlp
        elif isinstance(otlp, str):
            payload = otlp.encode("utf-8")
        else:
            with open(otlp, "rb") as f:
                payload = f.read()
        return _engine_convert(payload, agent_id)


def _engine_convert(payload: bytes, agent_id: str | None) -> Trace:
    """Run ``attest-engine trace convert -`` on an OTLP/JSON payload."""
    # Imported here: engine_manager imports the attest package, which imports
    # this module.
    from attest.engine_manager import resolve_engine

    cmd = [resolve_engine(), "trace", "convert"]
    if agent_id is not None:
        cmd += ["--agent-id", agent_id]
    cmd.append("-")
    try:
        proc = subprocess.run(
            cmd, input=payload, capture_output=True, timeout=_CONVERT_TIMEOUT_S, check=False
        )
    except subprocess.TimeoutExpired as exc:
        raise OTLPConversionError(
            f"attest-engine trace convert timed out after {_CONVERT_TIMEOUT_S}s"
        ) from exc
    if proc.returncode != 0:
        message = proc.stderr.decode("utf-8", errors="replace").strip()
        if not message:
            message = f"attest-engine trace convert exited {proc.returncode}"
        raise OTLPConversionError(message)
    try:
        data: dict[str, Any] = json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise OTLPConversionError(
            f"attest-engine trace convert printed output that is not JSON: {exc}"
        ) from exc
    return Trace.from_dict(data)


# ---------------------------------------------------------------------------
# OTLP/JSON encoding
# ---------------------------------------------------------------------------


def _otlp_document(spans: list[dict[str, Any]]) -> dict[str, Any]:
    return {"resourceSpans": [{"scopeSpans": [{"spans": spans}]}]}


def _any_value(value: Any) -> dict[str, Any]:
    """Encode an attribute value as an OTLP AnyValue."""
    if isinstance(value, bool):
        return {"boolValue": value}
    if isinstance(value, int):
        return {"intValue": str(value)}
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError(f"OTel attribute value must be finite, got {value!r}")
        return {"doubleValue": value}
    if isinstance(value, str):
        return {"stringValue": value}
    if isinstance(value, bytes):
        return {"bytesValue": base64.b64encode(value).decode("ascii")}
    if isinstance(value, Mapping):
        return {"kvlistValue": {"values": _key_values(value)}}
    if isinstance(value, Sequence):
        return {"arrayValue": {"values": [_any_value(v) for v in value]}}
    raise TypeError(f"OTel attribute value of type {type(value).__name__} is not supported")


def _key_values(attrs: Mapping[str, Any]) -> list[dict[str, Any]]:
    return [{"key": str(k), "value": _any_value(v)} for k, v in attrs.items()]


def _readable_spans_to_otlp(spans: Sequence[ReadableSpan]) -> dict[str, Any]:
    out: list[dict[str, Any]] = []
    for span in spans:
        ctx = span.context
        if ctx is None:
            raise ValueError(f"span {span.name!r} has no span context")
        encoded: dict[str, Any] = {
            "traceId": format(ctx.trace_id, "032x"),
            "spanId": format(ctx.span_id, "016x"),
            "name": span.name,
            "startTimeUnixNano": str(span.start_time or 0),
            "endTimeUnixNano": str(span.end_time or 0),
            "attributes": _key_values(span.attributes or {}),
        }
        if span.parent is not None:
            encoded["parentSpanId"] = format(span.parent.span_id, "016x")
        if span.status.status_code.value == _STATUS_CODE_ERROR:
            encoded["status"] = {
                "code": _STATUS_CODE_ERROR,
                "message": span.status.description or "",
            }
        out.append(encoded)
    return _otlp_document(out)


def _span_dicts_to_otlp(span_dicts: Sequence[Mapping[str, Any]]) -> dict[str, Any]:
    out: list[dict[str, Any]] = []
    for d in span_dicts:
        attrs = d.get("attributes") or {}
        if not isinstance(attrs, Mapping):
            raise ValueError(
                f"OTLP span dict 'attributes' must be a mapping; got {type(attrs).__name__}"
            )
        encoded: dict[str, Any] = {
            "traceId": d.get("trace_id") or "",
            "spanId": d.get("span_id") or "",
            "name": str(d.get("name", "")),
            "startTimeUnixNano": str(d.get("start_time_unix_nano", 0)),
            "endTimeUnixNano": str(d.get("end_time_unix_nano", 0)),
            "attributes": _key_values(attrs),
        }
        if d.get("parent_span_id"):
            encoded["parentSpanId"] = d["parent_span_id"]
        if d.get("status"):
            encoded["status"] = d["status"]
        out.append(encoded)
    return _otlp_document(out)


# ---------------------------------------------------------------------------
# Export
# ---------------------------------------------------------------------------

# Canonical operation name per step type for the export side. A producer may
# override it with the original operation in Step.args["operation"].
_STEP_TO_OPERATION: dict[str, str] = {
    STEP_LLM_CALL: "chat",
    STEP_TOOL_CALL: "execute_tool",
    STEP_AGENT_CALL: "invoke_agent",
    STEP_RETRIEVAL: "retrieval",
}

_ROOT_SPAN_ID = f"{1:016x}"


def to_otel_spans(trace: Trace) -> list[dict[str, Any]]:
    """Export an Attest Trace as OTLP-shaped span dicts with gen_ai.* attributes.

    The first span is an ``invoke_agent`` root for the trace's agent
    (``gen_ai.agent.name`` = ``trace.agent_id`` when set); every step becomes
    a child span. Steps run back to back from time 0 using
    ``metadata["duration_ms"]``. Each dict has ``trace_id``, ``span_id``,
    ``parent_span_id``, ``name``, ``kind``, ``start_time_unix_nano``,
    ``end_time_unix_nano``, ``attributes`` and, for failed steps, ``status``.
    :meth:`OTelAdapter.from_otel_span_dicts` converts them back.

    Mapping (current GenAI semantic conventions):
      - llm_call   → ``chat`` (or ``args["operation"]``), request/response
        model, ``gen_ai.usage.*`` tokens and ``gen_ai.usage.cost``
      - tool_call  → ``execute_tool``, ``gen_ai.tool.name``,
        ``gen_ai.tool.call.id``, ``gen_ai.tool.call.arguments`` /
        ``gen_ai.tool.call.result`` (JSON of ``args`` / ``result``)
      - agent_call → ``invoke_agent`` with ``gen_ai.agent.*``
      - retrieval  → ``retrieval`` with ``gen_ai.retrieval.*``
      - ``Step.error`` → status code 2 and ``error.type``

    Not exported: ``Trace.input``, ``Trace.output``, turns, handoffs, retries
    and recorded exchanges; the engine derives turns, handoffs and retries
    again from the span structure. Model and retrieval steps come back named
    after their span (``chat <model>``, ``retrieval``), and a tool result that
    is not a JSON object comes back under ``result``.
    """
    trace_id_hex = _derive_trace_id_hex(trace.trace_id)
    children: list[dict[str, Any]] = []
    cursor_ns = 0
    for index, step in enumerate(trace.steps, start=2):
        attrs = _step_to_attributes(step)
        duration_ns = 0
        meta = step.metadata or {}
        duration_ms = meta.get("duration_ms") if isinstance(meta, dict) else None
        if isinstance(duration_ms, int) and duration_ms > 0:
            duration_ns = duration_ms * 1_000_000
        span: dict[str, Any] = {
            "trace_id": trace_id_hex,
            "span_id": f"{index:016x}",
            "parent_span_id": _ROOT_SPAN_ID,
            "name": _step_to_span_name(step, attrs),
            "kind": "INTERNAL",
            "start_time_unix_nano": cursor_ns,
            "end_time_unix_nano": cursor_ns + duration_ns,
            "attributes": attrs,
        }
        if step.error is not None:
            span["status"] = {"code": _STATUS_CODE_ERROR, "message": step.error.message or ""}
        children.append(span)
        cursor_ns += duration_ns

    root_attrs: dict[str, Any] = {"gen_ai.operation.name": "invoke_agent"}
    if trace.agent_id:
        root_attrs["gen_ai.agent.name"] = trace.agent_id
    root: dict[str, Any] = {
        "trace_id": trace_id_hex,
        "span_id": _ROOT_SPAN_ID,
        "parent_span_id": None,
        "name": f"invoke_agent {trace.agent_id}" if trace.agent_id else "invoke_agent",
        "kind": "INTERNAL",
        "start_time_unix_nano": 0,
        "end_time_unix_nano": cursor_ns,
        "attributes": root_attrs,
    }
    return [root, *children]


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
    return hashlib.sha256(trace_id.encode("utf-8")).hexdigest()[:32]


def _copy(attrs: dict[str, Any], source: Mapping[str, Any], mapping: Mapping[str, str]) -> None:
    """Copy present keys of ``source`` into ``attrs`` under their attribute names."""
    for key, attr in mapping.items():
        if key in source:
            attrs[attr] = source[key]


def _step_to_attributes(step: Any) -> dict[str, Any]:
    """Build the gen_ai.* attribute dict for a single Step."""
    attrs: dict[str, Any] = {}
    args = step.args if isinstance(step.args, dict) else {}
    result = step.result if isinstance(step.result, dict) else {}
    attrs["gen_ai.operation.name"] = args.get("operation") or _STEP_TO_OPERATION.get(
        step.type, "chat"
    )
    if step.agent_id is not None:
        attrs["gen_ai.agent.id"] = step.agent_id
    if step.agent_role is not None:
        attrs["gen_ai.agent.name"] = step.agent_role
    if step.error is not None:
        attrs["error.type"] = step.error.type

    if step.type == STEP_LLM_CALL:
        _copy(
            attrs,
            args,
            {
                "model": "gen_ai.request.model",
                "system": "gen_ai.system",
                "provider": "gen_ai.provider.name",
                "prompt": "gen_ai.prompt",
            },
        )
        _copy(
            attrs,
            result,
            {
                "completion": "gen_ai.completion",
                "input_tokens": "gen_ai.usage.input_tokens",
                "output_tokens": "gen_ai.usage.output_tokens",
                "cost_usd": "gen_ai.usage.cost",
                "model": "gen_ai.response.model",
                "finish_reasons": "gen_ai.response.finish_reasons",
            },
        )
    elif step.type == STEP_TOOL_CALL:
        attrs["gen_ai.tool.name"] = step.name
        if step.tool_call_id:
            attrs["gen_ai.tool.call.id"] = step.tool_call_id
        if step.args is not None:
            attrs["gen_ai.tool.call.arguments"] = json.dumps(step.args, sort_keys=True)
        if step.result is not None:
            attrs["gen_ai.tool.call.result"] = json.dumps(step.result, sort_keys=True)
    elif step.type == STEP_AGENT_CALL:
        _copy(
            attrs,
            args,
            {
                "agent_id": "gen_ai.agent.id",
                "agent_name": "gen_ai.agent.name",
                "description": "gen_ai.agent.description",
                "prompt": "gen_ai.prompt",
            },
        )
        _copy(attrs, result, {"completion": "gen_ai.completion"})
    elif step.type == STEP_RETRIEVAL:
        _copy(
            attrs,
            args,
            {"query": "gen_ai.retrieval.query", "source": "gen_ai.retrieval.source"},
        )
        _copy(
            attrs,
            result,
            {
                "documents_count": "gen_ai.retrieval.documents.count",
                "documents": "gen_ai.retrieval.documents",
            },
        )
    return attrs


def _step_to_span_name(step: Any, attrs: dict[str, Any]) -> str:
    """Span name following the GenAI ``<operation> <target>`` convention."""
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
