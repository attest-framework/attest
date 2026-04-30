from __future__ import annotations

from attest._proto.types import (
    ERR_ASSERTION_ERROR,
    ERR_ENGINE_ERROR,
    ERR_INVALID_TRACE,
    ERR_PROVIDER_ERROR,
    ERR_SESSION_ERROR,
    ERR_TIMEOUT,
    STATUS_HARD_FAIL,
    STATUS_PASS,
    STATUS_SOFT_FAIL,
    STEP_AGENT_CALL,
    STEP_LLM_CALL,
    STEP_RETRIEVAL,
    STEP_TOOL_CALL,
    TYPE_CONSTRAINT,
    TYPE_CONTENT,
    TYPE_EMBEDDING,
    TYPE_LLM_JUDGE,
    TYPE_SCHEMA,
    TYPE_TRACE,
    AssertionResult,
    EvaluateBatchResult,
    InitializeParams,
    JudgeMetadata,
    Step,
    Trace,
    TraceMetadata,
)


def _make_trace() -> Trace:
    return Trace(
        trace_id="trc_abc123",
        output={
            "message": "Your refund has been processed.",
            "structured": {"refund_id": "RFD-001"},
        },
        schema_version=1,
        agent_id="customer-service-agent",
        input={"user_message": "I want a refund"},
        steps=[
            Step(
                type=STEP_LLM_CALL,
                name="reasoning",
                args={"model": "gpt-4.1", "prompt": "You are a customer service agent."},
                result={"completion": "I need to look up the order."},
                metadata={"duration_ms": 1200, "cost_usd": 0.003},
                started_at_ms=1700000000000,
                ended_at_ms=1700000001200,
                agent_id="customer-service-agent",
                agent_role="primary",
            ),
            Step(
                type=STEP_TOOL_CALL,
                name="lookup_order",
                args={"order_id": "ORD-123"},
                result={"status": "delivered", "amount": 89.99},
                metadata={"duration_ms": 45},
                started_at_ms=1700000001200,
                ended_at_ms=1700000001245,
                agent_id="customer-service-agent",
            ),
        ],
        metadata=TraceMetadata(
            total_tokens=1350,
            cost_usd=0.0067,
            latency_ms=4200,
            model="gpt-4.1",
            timestamp="2026-02-18T10:30:00Z",
        ),
        parent_trace_id=None,
    )


def test_trace_round_trip() -> None:
    original = _make_trace()
    d = original.to_dict()
    restored = Trace.from_dict(d)

    assert restored.trace_id == original.trace_id
    assert restored.schema_version == original.schema_version
    assert restored.agent_id == original.agent_id
    assert restored.output == original.output
    assert restored.input == original.input
    assert restored.parent_trace_id == original.parent_trace_id
    assert len(restored.steps) == len(original.steps)

    for orig_step, rest_step in zip(original.steps, restored.steps):
        assert rest_step.type == orig_step.type
        assert rest_step.name == orig_step.name
        assert rest_step.args == orig_step.args
        assert rest_step.result == orig_step.result
        assert rest_step.metadata == orig_step.metadata
        assert rest_step.started_at_ms == orig_step.started_at_ms
        assert rest_step.ended_at_ms == orig_step.ended_at_ms
        assert rest_step.agent_id == orig_step.agent_id
        assert rest_step.agent_role == orig_step.agent_role

    assert restored.metadata is not None
    assert original.metadata is not None
    assert restored.metadata.total_tokens == original.metadata.total_tokens
    assert restored.metadata.cost_usd == original.metadata.cost_usd
    assert restored.metadata.latency_ms == original.metadata.latency_ms
    assert restored.metadata.model == original.metadata.model
    assert restored.metadata.timestamp == original.metadata.timestamp


def test_assertion_result_round_trip() -> None:
    original = AssertionResult(
        assertion_id="assert_001",
        status=STATUS_PASS,
        score=1.0,
        explanation="Schema validation passed.",
        cost=0.0,
        duration_ms=2,
        request_id="req_key_001",
    )
    d = original.to_dict()
    restored = AssertionResult.from_dict(d)

    assert restored.assertion_id == original.assertion_id
    assert restored.status == original.status
    assert restored.score == original.score
    assert restored.explanation == original.explanation
    assert restored.cost == original.cost
    assert restored.duration_ms == original.duration_ms
    assert restored.request_id == original.request_id


def test_assertion_result_diagnostic_fields_round_trip() -> None:
    original = AssertionResult(
        assertion_id="judge_001",
        status=STATUS_HARD_FAIL,
        score=0.4,
        explanation="weak",
        cost=0.012,
        duration_ms=1820,
        request_id="req_001",
        threshold_source="dynamic",
        layer=6,
        type=TYPE_LLM_JUDGE,
        trace_node_path="output.answer",
        expected="judge_score >= 0.80",
        actual="judge_score=0.40",
        suggested_action="Calibrate the judge.",
        judge_metadata=JudgeMetadata(
            model="gpt-4.1",
            rubric_name="correctness",
            rubric_version="v3",
            prompt_hash="abcd1234",
            sample_scores=[0.4, 0.4, 0.6],
            score_mean=0.466,
            score_stddev=0.115,
            high_variance=False,
        ),
    )
    d = original.to_dict()
    restored = AssertionResult.from_dict(d)

    assert restored.layer == 6
    assert restored.type == TYPE_LLM_JUDGE
    assert restored.trace_node_path == "output.answer"
    assert restored.expected == "judge_score >= 0.80"
    assert restored.actual == "judge_score=0.40"
    assert restored.suggested_action == "Calibrate the judge."
    assert restored.threshold_source == "dynamic"
    assert restored.judge_metadata is not None
    assert restored.judge_metadata.model == "gpt-4.1"
    assert restored.judge_metadata.rubric_name == "correctness"
    assert restored.judge_metadata.sample_scores == [0.4, 0.4, 0.6]


def test_judge_metadata_bias_probes_roundtrip() -> None:
    """BiasProbe and JudgeAgreement round-trip through to_dict/from_dict."""
    from attest._proto.types import BiasProbe, JudgeAgreement

    original = JudgeMetadata(
        model="gpt-4.1",
        rubric_name="default",
        rubric_version="v1",
        prompt_hash="abcd1234",
        sample_scores=[0.5],
        score_mean=0.5,
        bias_probes=[
            BiasProbe(name="verbosity", score=0.7, delta=0.2),
            BiasProbe(name="position", score=0.55, delta=0.05),
        ],
        calibration=JudgeAgreement(label_count=10, agreement=0.8, cohen_kappa=0.6, roc_auc=0.9),
    )
    restored = JudgeMetadata.from_dict(original.to_dict())
    assert len(restored.bias_probes) == 2
    assert restored.bias_probes[0].name == "verbosity"
    assert restored.bias_probes[0].delta == 0.2
    assert restored.calibration is not None
    assert restored.calibration.cohen_kappa == 0.6
    assert restored.calibration.label_count == 10


def test_assertion_result_legacy_payload_loads() -> None:
    """Engine v1 payloads (no diagnostic fields) must still parse."""
    legacy: dict = {
        "assertion_id": "old",
        "status": STATUS_PASS,
        "score": 1.0,
        "explanation": "ok",
        "cost": 0.0,
        "duration_ms": 1,
    }
    result = AssertionResult.from_dict(legacy)
    assert result.layer == 0
    assert result.type is None
    assert result.judge_metadata is None


def test_initialize_params_to_dict() -> None:
    params = InitializeParams(
        sdk_name="attest-python",
        sdk_version="0.1.0",
        protocol_version=1,
        required_capabilities=["layers_1_4", "soft_failures"],
        preferred_encoding="json",
    )
    d = params.to_dict()

    assert d["sdk_name"] == "attest-python"
    assert d["sdk_version"] == "0.1.0"
    assert d["protocol_version"] == 1
    assert d["required_capabilities"] == ["layers_1_4", "soft_failures"]
    assert d["preferred_encoding"] == "json"


def test_evaluate_batch_result_from_dict() -> None:
    sample: dict = {
        "results": [
            {
                "assertion_id": "assert_001",
                "status": "pass",
                "score": 1.0,
                "explanation": "Tool result matches schema.",
                "cost": 0.0,
                "duration_ms": 2,
                "request_id": "req_key_001",
            },
            {
                "assertion_id": "assert_006",
                "status": "pass",
                "score": 0.91,
                "explanation": "Helpfulness score 0.91 exceeds threshold.",
                "cost": 0.0012,
                "duration_ms": 1840,
                "request_id": "req_key_006",
            },
        ],
        "total_cost": 0.0012,
        "total_duration_ms": 1845,
    }
    result = EvaluateBatchResult.from_dict(sample)

    assert len(result.results) == 2
    assert result.total_cost == 0.0012
    assert result.total_duration_ms == 1845
    assert result.results[0].assertion_id == "assert_001"
    assert result.results[0].status == STATUS_PASS
    assert result.results[1].assertion_id == "assert_006"
    assert result.results[1].score == 0.91
    assert result.results[1].cost == 0.0012


def test_constants() -> None:
    # Step types
    assert STEP_LLM_CALL == "llm_call"
    assert STEP_TOOL_CALL == "tool_call"
    assert STEP_RETRIEVAL == "retrieval"
    assert STEP_AGENT_CALL == "agent_call"

    # Status
    assert STATUS_PASS == "pass"
    assert STATUS_SOFT_FAIL == "soft_fail"
    assert STATUS_HARD_FAIL == "hard_fail"

    # Assertion types
    assert TYPE_SCHEMA == "schema"
    assert TYPE_CONSTRAINT == "constraint"
    assert TYPE_TRACE == "trace"
    assert TYPE_CONTENT == "content"
    assert TYPE_EMBEDDING == "embedding"
    assert TYPE_LLM_JUDGE == "llm_judge"

    # Error codes
    assert ERR_INVALID_TRACE == 1001
    assert ERR_ASSERTION_ERROR == 1002
    assert ERR_PROVIDER_ERROR == 2001
    assert ERR_ENGINE_ERROR == 3001
    assert ERR_TIMEOUT == 3002
    assert ERR_SESSION_ERROR == 3003
