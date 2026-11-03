from __future__ import annotations

from collections.abc import Iterable
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
STATUS_NOT_EVALUATED: str = "not_evaluated"
KNOWN_STATUSES: frozenset[str] = frozenset(
    {STATUS_PASS, STATUS_SOFT_FAIL, STATUS_HARD_FAIL, STATUS_NOT_EVALUATED}
)

# Reasons carried by a not_evaluated result (AssertionResult.reason).
REASON_PROVIDER_UNAVAILABLE: str = "provider_unavailable"
REASON_PROVIDER_NOT_CONFIGURED: str = "provider_not_configured"
REASON_SIMULATION: str = "simulation"
REASON_MISSING_EVIDENCE: str = "missing_evidence"
REASON_SHORT_CIRCUITED: str = "short_circuited"
REASON_BUDGET_EXHAUSTED: str = "budget_exhausted"
REASON_JUDGE_ABSTAINED: str = "judge_abstained"
REASON_REPLAY_MISS: str = "replay_miss"

# ---------------------------------------------------------------------------
# Assertion type constants
# ---------------------------------------------------------------------------

TYPE_SCHEMA: str = "schema"
TYPE_CONSTRAINT: str = "constraint"
TYPE_TRACE: str = "trace"
TYPE_EFFECT: str = "effect"
TYPE_CONTENT: str = "content"
TYPE_EMBEDDING: str = "embedding"
TYPE_LLM_JUDGE: str = "llm_judge"
TYPE_TRACE_TREE: str = "trace_tree"
TYPE_PLUGIN: str = "plugin"

# ---------------------------------------------------------------------------
# FailureClass constants
# ---------------------------------------------------------------------------

# Mirrors engine/pkg/types/failure_class.go. Empty string ("") means
# "not classified" (passes and results the heuristic declined to label).
FAILURE_CLASS_BROKEN_CODE: str = "broken_code"
FAILURE_CLASS_FLAKY_JUDGE: str = "flaky_judge"
FAILURE_CLASS_BAD_RUBRIC: str = "bad_rubric"
FAILURE_CLASS_MISSING_TRACE_DATA: str = "missing_trace_data"
FAILURE_CLASS_STOCHASTIC_VARIANCE: str = "stochastic_variance"

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
class StepError:
    """Trace v2: a step whose span ended in error."""

    type: str
    message: str | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"type": self.type}
        if self.message:
            d["message"] = self.message
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> StepError:
        return cls(type=data["type"], message=data.get("message"))


@dataclass
class Exchange:
    """Trace v2: a recorded model request/response pair."""

    request: dict[str, Any]
    response: dict[str, Any]

    def to_dict(self) -> dict[str, Any]:
        return {"request": _serialize(self.request), "response": _serialize(self.response)}

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Exchange:
        return cls(request=data["request"], response=data["response"])


@dataclass
class Turn:
    """Trace v2: one conversation turn and the indices of its steps."""

    index: int
    step_indices: list[int] = field(default_factory=list)
    span_id: str | None = None
    input: Any = None
    output: Any = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"index": self.index}
        if self.span_id:
            d["span_id"] = self.span_id
        if self.input is not None:
            d["input"] = _serialize(self.input)
        if self.output is not None:
            d["output"] = _serialize(self.output)
        d["step_indices"] = list(self.step_indices)
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Turn:
        return cls(
            index=data["index"],
            step_indices=list(data.get("step_indices") or []),
            span_id=data.get("span_id"),
            input=data.get("input"),
            output=data.get("output"),
        )


@dataclass
class Handoff:
    """Trace v2: control passing from one agent to another at an agent_call step."""

    from_agent: str
    to_agent: str
    step_index: int

    def to_dict(self) -> dict[str, Any]:
        return {
            "from_agent": self.from_agent,
            "to_agent": self.to_agent,
            "step_index": self.step_index,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Handoff:
        return cls(
            from_agent=data["from_agent"],
            to_agent=data["to_agent"],
            step_index=data["step_index"],
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
    # Trace v2 (schema_version 2) fields.
    span_id: str | None = None
    parent_span_id: str | None = None
    tool_call_id: str | None = None
    retry_of: int | None = None
    error: StepError | None = None
    exchange: Exchange | None = None

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
        if self.span_id:
            d["span_id"] = self.span_id
        if self.parent_span_id:
            d["parent_span_id"] = self.parent_span_id
        if self.tool_call_id:
            d["tool_call_id"] = self.tool_call_id
        if self.retry_of is not None:
            d["retry_of"] = self.retry_of
        if self.error is not None:
            d["error"] = self.error.to_dict()
        if self.exchange is not None:
            d["exchange"] = self.exchange.to_dict()
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
    # Trace v2 (schema_version 2) fields.
    conversation_id: str | None = None
    turns: list[Turn] = field(default_factory=list)
    handoffs: list[Handoff] = field(default_factory=list)

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
        if self.parent_trace_id is not None:
            d["parent_trace_id"] = self.parent_trace_id
        if self.conversation_id:
            d["conversation_id"] = self.conversation_id
        if self.turns:
            d["turns"] = [t.to_dict() for t in self.turns]
        if self.handoffs:
            d["handoffs"] = [h.to_dict() for h in self.handoffs]
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
                    span_id=s.get("span_id"),
                    parent_span_id=s.get("parent_span_id"),
                    tool_call_id=s.get("tool_call_id"),
                    retry_of=s.get("retry_of"),
                    error=StepError.from_dict(s["error"]) if s.get("error") else None,
                    exchange=Exchange.from_dict(s["exchange"]) if s.get("exchange") else None,
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
            conversation_id=data.get("conversation_id"),
            turns=[Turn.from_dict(t) for t in data.get("turns") or []],
            handoffs=[Handoff.from_dict(h) for h in data.get("handoffs") or []],
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
class BiasProbe:
    """One judge-bias probe result.

    Score is the score the judge produced on the mutated input; delta is
    score - baseline. A well-calibrated judge has |delta| close to zero
    across all probes. Mirrors engine/pkg/types/assertion.go::BiasProbe.
    """

    name: str
    score: float
    delta: float

    def to_dict(self) -> dict[str, Any]:
        return {"name": self.name, "score": self.score, "delta": self.delta}

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> BiasProbe:
        return cls(
            name=data["name"],
            score=data.get("score", 0.0),
            delta=data.get("delta", 0.0),
        )


@dataclass
class JudgeAgreement:
    """Calibration agreement summary for a judge configuration.

    Computed against a stored set of human labels for the
    (rubric_name, rubric_version, prompt_hash) key. Mirrors
    engine/pkg/types/assertion.go::JudgeAgreement.
    """

    label_count: int
    agreement: float
    cohen_kappa: float
    roc_auc: float = 0.0

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {
            "label_count": self.label_count,
            "agreement": self.agreement,
            "cohen_kappa": self.cohen_kappa,
        }
        if self.roc_auc:
            d["roc_auc"] = self.roc_auc
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> JudgeAgreement:
        return cls(
            label_count=data.get("label_count", 0),
            agreement=data.get("agreement", 0.0),
            cohen_kappa=data.get("cohen_kappa", 0.0),
            roc_auc=data.get("roc_auc", 0.0),
        )


@dataclass
class JudgeMetadata:
    """Audit trail for a single LLM-judge evaluation.

    Surfaces alongside an AssertionResult when the engine evaluates an
    llm_judge assertion. Empty for non-judge results. Fields mirror
    engine/pkg/types/assertion.go::JudgeMetadata so reports render the
    same data on both sides of the protocol.
    """

    model: str | None = None
    rubric_name: str | None = None
    rubric_version: str | None = None
    prompt_hash: str | None = None
    sample_scores: list[float] = field(default_factory=list)
    score_mean: float = 0.0
    score_stddev: float = 0.0
    high_variance: bool = False
    bias_probes: list[BiasProbe] = field(default_factory=list)
    calibration: JudgeAgreement | None = None
    provider: str | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {}
        if self.provider is not None:
            d["provider"] = self.provider
        if self.model is not None:
            d["model"] = self.model
        if self.rubric_name is not None:
            d["rubric_name"] = self.rubric_name
        if self.rubric_version is not None:
            d["rubric_version"] = self.rubric_version
        if self.prompt_hash is not None:
            d["prompt_hash"] = self.prompt_hash
        if self.sample_scores:
            d["sample_scores"] = list(self.sample_scores)
        if self.score_mean:
            d["score_mean"] = self.score_mean
        if self.score_stddev:
            d["score_stddev"] = self.score_stddev
        if self.high_variance:
            d["high_variance"] = self.high_variance
        if self.bias_probes:
            d["bias_probes"] = [p.to_dict() for p in self.bias_probes]
        if self.calibration is not None:
            d["calibration"] = self.calibration.to_dict()
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> JudgeMetadata:
        raw_probes = data.get("bias_probes", [])
        raw_cal = data.get("calibration")
        return cls(
            provider=data.get("provider"),
            model=data.get("model"),
            rubric_name=data.get("rubric_name"),
            rubric_version=data.get("rubric_version"),
            prompt_hash=data.get("prompt_hash"),
            sample_scores=list(data.get("sample_scores", [])),
            score_mean=data.get("score_mean", 0.0),
            score_stddev=data.get("score_stddev", 0.0),
            high_variance=data.get("high_variance", False),
            bias_probes=[BiasProbe.from_dict(p) for p in raw_probes],
            calibration=JudgeAgreement.from_dict(raw_cal) if raw_cal else None,
        )


@dataclass
class AssertionResult:
    assertion_id: str
    status: str
    score: float
    explanation: str
    cost: float = 0.0
    duration_ms: int = 0
    request_id: str | None = None
    reason: str | None = None
    threshold_source: str | None = None
    layer: int = 0
    type: str | None = None
    trace_node_path: str | None = None
    expected: str | None = None
    actual: str | None = None
    suggested_action: str | None = None
    judge_metadata: JudgeMetadata | None = None
    failure_class: str | None = None
    # 1-based trial index, set by the SDK when the test ran as fixed-N trials.
    trial: int | None = None

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
        if self.reason is not None:
            d["reason"] = self.reason
        if self.threshold_source is not None:
            d["threshold_source"] = self.threshold_source
        if self.layer:
            d["layer"] = self.layer
        if self.type is not None:
            d["type"] = self.type
        if self.trace_node_path is not None:
            d["trace_node_path"] = self.trace_node_path
        if self.expected is not None:
            d["expected"] = self.expected
        if self.actual is not None:
            d["actual"] = self.actual
        if self.suggested_action is not None:
            d["suggested_action"] = self.suggested_action
        if self.judge_metadata is not None:
            d["judge_metadata"] = self.judge_metadata.to_dict()
        if self.failure_class is not None:
            d["failure_class"] = self.failure_class
        if self.trial is not None:
            d["trial"] = self.trial
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> AssertionResult:
        raw_judge = data.get("judge_metadata")
        judge = JudgeMetadata.from_dict(raw_judge) if raw_judge else None
        status = data["status"]
        explanation = data["explanation"]
        if status not in KNOWN_STATUSES:
            # Protocol rule: an unknown status is never a pass.
            explanation = f"[unknown status {status!r}] {explanation}"
            status = STATUS_NOT_EVALUATED
        return cls(
            assertion_id=data["assertion_id"],
            status=status,
            score=data["score"],
            explanation=explanation,
            cost=data.get("cost", 0.0),
            duration_ms=data.get("duration_ms", 0),
            request_id=data.get("request_id"),
            reason=data.get("reason"),
            threshold_source=data.get("threshold_source"),
            layer=data.get("layer", 0),
            type=data.get("type"),
            trace_node_path=data.get("trace_node_path"),
            expected=data.get("expected"),
            actual=data.get("actual"),
            suggested_action=data.get("suggested_action"),
            judge_metadata=judge,
            failure_class=data.get("failure_class"),
            trial=data.get("trial"),
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


USAGE_FIELDS: tuple[str, ...] = (
    "model_calls",
    "judge_calls",
    "input_tokens",
    "output_tokens",
    "cached_tokens",
    "cost_usd",
    "retry_cost_usd",
    "unpriced_calls",
    "budget_refused",
)


@dataclass(frozen=True)
class Usage:
    """Attest-initiated model usage of one or more ``evaluate_batch`` calls.

    Counts L5 embeddings, L6 judges, judge repeats, bias probes and
    simulated-user messages; agent calls not routed through attest are not
    counted. ``judge_calls`` is the judge-provider part of ``model_calls``.
    ``retry_cost_usd`` is the part of ``cost_usd`` spent on retried calls;
    ``unpriced_calls`` counts calls whose model has no price (``cost_usd``
    understates the spend when it is non-zero); ``budget_refused`` counts
    calls not sent because no budget remained.
    """

    model_calls: int = 0
    judge_calls: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    cached_tokens: int = 0
    cost_usd: float = 0.0
    retry_cost_usd: float = 0.0
    unpriced_calls: int = 0
    budget_refused: int = 0

    def __add__(self, other: Usage) -> Usage:
        return Usage(
            model_calls=self.model_calls + other.model_calls,
            judge_calls=self.judge_calls + other.judge_calls,
            input_tokens=self.input_tokens + other.input_tokens,
            output_tokens=self.output_tokens + other.output_tokens,
            cached_tokens=self.cached_tokens + other.cached_tokens,
            cost_usd=self.cost_usd + other.cost_usd,
            retry_cost_usd=self.retry_cost_usd + other.retry_cost_usd,
            unpriced_calls=self.unpriced_calls + other.unpriced_calls,
            budget_refused=self.budget_refused + other.budget_refused,
        )

    @classmethod
    def total(cls, usages: Iterable[Usage]) -> Usage:
        """Sum of ``usages`` (all-zero when empty)."""
        acc = cls()
        for u in usages:
            acc = acc + u
        return acc

    def format(self) -> str:
        """The run-summary text, identical to the engine's ``report.FormatUsage``:
        model calls · tokens (cached) · $ · retry cost, with refused and unpriced
        calls appended when present."""
        line = (
            f"{self.model_calls} model calls · {self.input_tokens + self.output_tokens} tokens "
            f"({self.cached_tokens} cached) · ${self.cost_usd:.4f} · "
            f"retry cost ${self.retry_cost_usd:.4f}"
        )
        if self.budget_refused > 0:
            line += f" · {self.budget_refused} calls refused by budget"
        if self.unpriced_calls > 0:
            line += f" · {self.unpriced_calls} unpriced calls not in $"
        return line

    def to_dict(self) -> dict[str, Any]:
        return {name: getattr(self, name) for name in USAGE_FIELDS}

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Usage:
        """Parse a wire ``usage`` object; every field is required."""
        missing = [name for name in USAGE_FIELDS if name not in data]
        if missing:
            raise ValueError(f"usage is missing required fields: {', '.join(missing)}")
        return cls(
            model_calls=int(data["model_calls"]),
            judge_calls=int(data["judge_calls"]),
            input_tokens=int(data["input_tokens"]),
            output_tokens=int(data["output_tokens"]),
            cached_tokens=int(data["cached_tokens"]),
            cost_usd=float(data["cost_usd"]),
            retry_cost_usd=float(data["retry_cost_usd"]),
            unpriced_calls=int(data["unpriced_calls"]),
            budget_refused=int(data["budget_refused"]),
        )


@dataclass(frozen=True)
class BatchBudget:
    """``evaluate_batch`` ``budget`` param: limits for one batch on top of the
    session budget. ``None`` means no limit for that field."""

    max_judge_calls: int | None = None
    max_cost_usd: float | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {}
        if self.max_judge_calls is not None:
            d["max_judge_calls"] = self.max_judge_calls
        if self.max_cost_usd is not None:
            d["max_cost_usd"] = self.max_cost_usd
        return d


@dataclass
class EvaluateBatchResult:
    results: list[AssertionResult]
    usage: Usage
    total_cost: float = 0.0
    total_duration_ms: int = 0
    simulated: bool = False

    def to_dict(self) -> dict[str, Any]:
        return {
            "results": [r.to_dict() for r in self.results],
            "usage": self.usage.to_dict(),
            "total_cost": self.total_cost,
            "total_duration_ms": self.total_duration_ms,
            "simulated": self.simulated,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> EvaluateBatchResult:
        results = [AssertionResult.from_dict(r) for r in data.get("results", [])]
        if "usage" not in data:
            raise ValueError("evaluate_batch result is missing required field 'usage'")
        return cls(
            results=results,
            usage=Usage.from_dict(data["usage"]),
            total_cost=data.get("total_cost", 0.0),
            total_duration_ms=data.get("total_duration_ms", 0),
            simulated=data.get("simulated", False),
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


# ---------------------------------------------------------------------------
# Drift detection types
# ---------------------------------------------------------------------------


@dataclass
class DriftReport:
    assertion_id: str
    mean: float
    stddev: float
    count: int
    latest_score: float
    deviation: float
    status: str

    def to_dict(self) -> dict[str, Any]:
        return {
            "assertion_id": self.assertion_id,
            "mean": self.mean,
            "stddev": self.stddev,
            "count": self.count,
            "latest_score": self.latest_score,
            "deviation": self.deviation,
            "status": self.status,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> DriftReport:
        return cls(
            assertion_id=data["assertion_id"],
            mean=data["mean"],
            stddev=data["stddev"],
            count=data["count"],
            latest_score=data["latest_score"],
            deviation=data["deviation"],
            status=data["status"],
        )


# ---------------------------------------------------------------------------
# Simulation types
# ---------------------------------------------------------------------------


@dataclass
class ConversationMessage:
    role: str
    content: str

    def to_dict(self) -> dict[str, Any]:
        return {"role": self.role, "content": self.content}

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> ConversationMessage:
        return cls(role=data["role"], content=data["content"])


@dataclass
class SimulatePersona:
    name: str
    system_prompt: str
    style: str
    temperature: float
    max_tokens: int | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {
            "name": self.name,
            "system_prompt": self.system_prompt,
            "style": self.style,
            "temperature": self.temperature,
        }
        if self.max_tokens is not None:
            d["max_tokens"] = self.max_tokens
        return d


@dataclass
class SimulateFaultConfig:
    error_rate: float = 0.0
    latency_jitter_ms: int = 0
    content_corruption: bool = False
    timeout_after_ms: int = 0

    def to_dict(self) -> dict[str, Any]:
        return {
            "error_rate": self.error_rate,
            "latency_jitter_ms": self.latency_jitter_ms,
            "content_corruption": self.content_corruption,
            "timeout_after_ms": self.timeout_after_ms,
        }


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
