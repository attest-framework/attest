from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

# ---------------------------------------------------------------------------
# Step type constants
# ---------------------------------------------------------------------------

STEP_LLM_CALL: str = "llm_call"
STEP_TOOL_CALL: str = "tool_call"
STEP_RETRIEVAL: str = "retrieval"
STEP_AGENT_CALL: str = "agent_call"

# ---------------------------------------------------------------------------
# Status constants
# ---------------------------------------------------------------------------

STATUS_PASS: str = "pass"
STATUS_SOFT_FAIL: str = "soft_fail"
STATUS_HARD_FAIL: str = "hard_fail"

# ---------------------------------------------------------------------------
# Assertion type constants
# ---------------------------------------------------------------------------

TYPE_SCHEMA: str = "schema"
TYPE_CONSTRAINT: str = "constraint"
TYPE_TRACE: str = "trace"
TYPE_CONTENT: str = "content"
TYPE_EMBEDDING: str = "embedding"
TYPE_LLM_JUDGE: str = "llm_judge"
TYPE_TRACE_TREE: str = "trace_tree"

# ---------------------------------------------------------------------------
# Error code constants
# ---------------------------------------------------------------------------

ERR_INVALID_TRACE: int = 1001
ERR_ASSERTION_ERROR: int = 1002
ERR_PROVIDER_ERROR: int = 2001
ERR_ENGINE_ERROR: int = 3001
ERR_TIMEOUT: int = 3002
ERR_SESSION_ERROR: int = 3003


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _serialize(value: Any) -> Any:
    """Recursively convert dataclass instances to dicts for JSON serialization."""
    if hasattr(value, "to_dict"):
        return value.to_dict()
    if isinstance(value, list):
        return [_serialize(v) for v in value]
    if isinstance(value, dict):
        return {k: _serialize(v) for k, v in value.items()}
    return value


# ---------------------------------------------------------------------------
# Dataclasses
# ---------------------------------------------------------------------------


@dataclass
class TraceMetadata:
    total_tokens: int | None = None
    cost_usd: float | None = None
    latency_ms: int | None = None
    model: str | None = None
    timestamp: str | None = None

    def to_dict(self) -> dict[str, Any]:
        result: dict[str, Any] = {}
        if self.total_tokens is not None:
            result["total_tokens"] = self.total_tokens
        if self.cost_usd is not None:
            result["cost_usd"] = self.cost_usd
        if self.latency_ms is not None:
            result["latency_ms"] = self.latency_ms
        if self.model is not None:
            result["model"] = self.model
        if self.timestamp is not None:
            result["timestamp"] = self.timestamp
        return result

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> TraceMetadata:
        return cls(
            total_tokens=data.get("total_tokens"),
            cost_usd=data.get("cost_usd"),
            latency_ms=data.get("latency_ms"),
            model=data.get("model"),
            timestamp=data.get("timestamp"),
        )


@dataclass
class Step:
    type: str
    name: str
    args: dict[str, Any] | None = None
    result: dict[str, Any] | None = None
    sub_trace: Trace | None = None
    metadata: dict[str, Any] | None = None
    started_at_ms: int | None = None
    ended_at_ms: int | None = None
    agent_id: str | None = None
    agent_role: str | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"type": self.type, "name": self.name}
        if self.args is not None:
            d["args"] = _serialize(self.args)
        if self.result is not None:
            d["result"] = _serialize(self.result)
        if self.sub_trace is not None:
            d["sub_trace"] = self.sub_trace.to_dict()
        if self.metadata is not None:
            d["metadata"] = _serialize(self.metadata)
        if self.started_at_ms is not None:
            d["started_at_ms"] = self.started_at_ms
        if self.ended_at_ms is not None:
            d["ended_at_ms"] = self.ended_at_ms
        if self.agent_id is not None:
            d["agent_id"] = self.agent_id
        if self.agent_role is not None:
            d["agent_role"] = self.agent_role
        return d


@dataclass
class Trace:
    trace_id: str
    output: dict[str, Any]
    schema_version: int = 1
    agent_id: str | None = None
    input: dict[str, Any] | None = None
    steps: list[Step] = field(default_factory=list)
    metadata: TraceMetadata | None = None
    parent_trace_id: str | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {
            "schema_version": self.schema_version,
            "trace_id": self.trace_id,
            "output": _serialize(self.output),
        }
        if self.agent_id is not None:
            d["agent_id"] = self.agent_id
        if self.input is not None:
            d["input"] = _serialize(self.input)
        d["steps"] = [s.to_dict() for s in self.steps]
        if self.metadata is not None:
            d["metadata"] = self.metadata.to_dict()
        d["parent_trace_id"] = self.parent_trace_id
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Trace:
        raw_steps = data.get("steps") or []
        steps: list[Step] = []
        for s in raw_steps:
            sub_trace_data = s.get("sub_trace")
            sub_trace = Trace.from_dict(sub_trace_data) if sub_trace_data else None
            steps.append(
                Step(
                    type=s["type"],
                    name=s["name"],
                    args=s.get("args"),
                    result=s.get("result"),
                    sub_trace=sub_trace,
                    metadata=s.get("metadata"),
                    started_at_ms=s.get("started_at_ms"),
                    ended_at_ms=s.get("ended_at_ms"),
                    agent_id=s.get("agent_id"),
                    agent_role=s.get("agent_role"),
                )
            )
        raw_meta = data.get("metadata")
        metadata = TraceMetadata.from_dict(raw_meta) if raw_meta else None
        return cls(
            trace_id=data["trace_id"],
            output=data["output"],
            schema_version=data.get("schema_version", 1),
            agent_id=data.get("agent_id"),
            input=data.get("input"),
            steps=steps,
            metadata=metadata,
            parent_trace_id=data.get("parent_trace_id"),
        )


@dataclass
class Assertion:
    assertion_id: str
    type: str
    spec: dict[str, Any]
    request_id: str | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {
            "assertion_id": self.assertion_id,
            "type": self.type,
            "spec": _serialize(self.spec),
        }
        if self.request_id is not None:
            d["request_id"] = self.request_id
        return d


@dataclass
class AssertionResult:
    assertion_id: str
    status: str
    score: float
    explanation: str
    cost: float = 0.0
    duration_ms: int = 0
    request_id: str | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {
            "assertion_id": self.assertion_id,
            "status": self.status,
            "score": self.score,
            "explanation": self.explanation,
            "cost": self.cost,
            "duration_ms": self.duration_ms,
        }
        if self.request_id is not None:
            d["request_id"] = self.request_id
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> AssertionResult:
        return cls(
            assertion_id=data["assertion_id"],
            status=data["status"],
            score=data["score"],
            explanation=data["explanation"],
            cost=data.get("cost", 0.0),
            duration_ms=data.get("duration_ms", 0),
            request_id=data.get("request_id"),
        )


@dataclass
class InitializeParams:
    sdk_name: str
    sdk_version: str
    protocol_version: int
    required_capabilities: list[str]
    preferred_encoding: str = "json"

    def to_dict(self) -> dict[str, Any]:
        return {
            "sdk_name": self.sdk_name,
            "sdk_version": self.sdk_version,
            "protocol_version": self.protocol_version,
            "required_capabilities": list(self.required_capabilities),
            "preferred_encoding": self.preferred_encoding,
        }


@dataclass
class InitializeResult:
    engine_version: str
    protocol_version: int
    capabilities: list[str]
    missing: list[str]
    compatible: bool
    encoding: str = "json"
    max_concurrent_requests: int = 64
    max_trace_size_bytes: int = 10485760
    max_steps_per_trace: int = 10000

    def to_dict(self) -> dict[str, Any]:
        return {
            "engine_version": self.engine_version,
            "protocol_version": self.protocol_version,
            "capabilities": list(self.capabilities),
            "missing": list(self.missing),
            "compatible": self.compatible,
            "encoding": self.encoding,
            "max_concurrent_requests": self.max_concurrent_requests,
            "max_trace_size_bytes": self.max_trace_size_bytes,
            "max_steps_per_trace": self.max_steps_per_trace,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> InitializeResult:
        return cls(
            engine_version=data["engine_version"],
            protocol_version=data["protocol_version"],
            capabilities=data.get("capabilities", []),
            missing=data.get("missing", []),
            compatible=data["compatible"],
            encoding=data.get("encoding", "json"),
            max_concurrent_requests=data.get("max_concurrent_requests", 64),
            max_trace_size_bytes=data.get("max_trace_size_bytes", 10485760),
            max_steps_per_trace=data.get("max_steps_per_trace", 10000),
        )


@dataclass
class EvaluateBatchParams:
    trace: Trace
    assertions: list[Assertion]

    def to_dict(self) -> dict[str, Any]:
        return {
            "trace": self.trace.to_dict(),
            "assertions": [a.to_dict() for a in self.assertions],
        }


@dataclass
class EvaluateBatchResult:
    results: list[AssertionResult]
    total_cost: float = 0.0
    total_duration_ms: int = 0

    def to_dict(self) -> dict[str, Any]:
        return {
            "results": [r.to_dict() for r in self.results],
            "total_cost": self.total_cost,
            "total_duration_ms": self.total_duration_ms,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> EvaluateBatchResult:
        results = [AssertionResult.from_dict(r) for r in data.get("results", [])]
        return cls(
            results=results,
            total_cost=data.get("total_cost", 0.0),
            total_duration_ms=data.get("total_duration_ms", 0),
        )


@dataclass
class ShutdownResult:
    sessions_completed: int
    assertions_evaluated: int

    def to_dict(self) -> dict[str, Any]:
        return {
            "sessions_completed": self.sessions_completed,
            "assertions_evaluated": self.assertions_evaluated,
        }


@dataclass
class SubmitPluginResultParams:
    trace_id: str
    plugin_name: str
    assertion_id: str
    result: AssertionResult

    def to_dict(self) -> dict[str, Any]:
        return {
            "trace_id": self.trace_id,
            "plugin_name": self.plugin_name,
            "assertion_id": self.assertion_id,
            "result": self.result.to_dict(),
        }


@dataclass
class ErrorData:
    error_type: str
    retryable: bool
    detail: str

    def to_dict(self) -> dict[str, Any]:
        return {
            "error_type": self.error_type,
            "retryable": self.retryable,
            "detail": self.detail,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> ErrorData:
        return cls(
            error_type=data["error_type"],
            retryable=data["retryable"],
            detail=data["detail"],
        )


@dataclass
class RPCError:
    code: int
    message: str
    data: ErrorData | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"code": self.code, "message": self.message}
        if self.data is not None:
            d["data"] = self.data.to_dict()
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> RPCError:
        raw_data = data.get("data")
        error_data = ErrorData.from_dict(raw_data) if raw_data else None
        return cls(
            code=data["code"],
            message=data["message"],
            data=error_data,
        )
