"""End-to-end integration tests — Python SDK ↔ Go engine over stdio JSON-RPC.

These tests require a built engine binary. Build with:
    make engine

Run with:
    cd sdks/python && uv run pytest tests/integration/ -v -m integration
"""

from __future__ import annotations

import pytest

from attest._proto.types import (
    Assertion,
    STATUS_HARD_FAIL,
    STATUS_PASS,
    STATUS_SOFT_FAIL,
    TYPE_CONSTRAINT,
    TYPE_CONTENT,
    TYPE_SCHEMA,
    TYPE_TRACE,
)
from attest.expect import expect
from attest.plugin import AttestEngineFixture
from attest.result import AgentResult
from attest.trace import TraceBuilder


def _refund_trace() -> TraceBuilder:
    """Build a refund agent trace for e2e testing."""
    return (
        TraceBuilder(agent_id="refund-agent")
        .set_trace_id("trc_e2e_refund")
        .set_input(user_message="I want a refund for order ORD-456")
        .add_llm_call(
            "reasoning",
            args={"model": "gpt-4.1"},
            result={"completion": "Looking up order details."},
            metadata={"duration_ms": 800},
        )
        .add_tool_call(
            "lookup_order",
            args={"order_id": "ORD-456"},
            result={"status": "delivered", "amount": 49.99, "eligible_for_refund": True},
            metadata={"duration_ms": 30},
        )
        .add_tool_call(
            "process_refund",
            args={"order_id": "ORD-456", "amount": 49.99},
            result={"refund_id": "RFD-100", "estimated_days": 5},
            metadata={"duration_ms": 90},
        )
        .set_output(
            message="Your refund of $49.99 for order ORD-456 has been processed. Refund ID: RFD-100. Expect it within 5 business days.",
            structured={"refund_id": "RFD-100", "amount": 49.99},
        )
        .set_metadata(
            total_tokens=1100,
            cost_usd=0.005,
            latency_ms=3500,
            model="gpt-4.1",
        )
    )


@pytest.mark.integration
class TestFullRoundTrip:
    """Full pytest-to-engine round trip over stdio."""

    def test_all_layers_pass(self, engine: AttestEngineFixture) -> None:
        """All L1-L4 assertions pass against a well-formed refund trace."""
        trace = _refund_trace().build()
        assertions = [
            # L1: Schema — verify structured output has refund_id
            Assertion(
                assertion_id="e2e_schema_1",
                type=TYPE_SCHEMA,
                spec={
                    "target": "output.structured",
                    "schema": {
                        "type": "object",
                        "required": ["refund_id", "amount"],
                        "properties": {
                            "refund_id": {"type": "string"},
                            "amount": {"type": "number"},
                        },
                    },
                },
            ),
            # L2: Constraint — cost under $0.01
            Assertion(
                assertion_id="e2e_constraint_1",
                type=TYPE_CONSTRAINT,
                spec={"field": "metadata.cost_usd", "operator": "lte", "value": 0.01},
            ),
            # L3: Trace — tools in order
            Assertion(
                assertion_id="e2e_trace_1",
                type=TYPE_TRACE,
                spec={
                    "check": "contains_in_order",
                    "tools": ["lookup_order", "process_refund"],
                },
            ),
            # L4: Content — output mentions refund
            Assertion(
                assertion_id="e2e_content_1",
                type=TYPE_CONTENT,
                spec={
                    "target": "output.message",
                    "check": "contains",
                    "value": "refund",
                },
            ),
        ]

        result = engine.evaluate(
            _chain_from_raw(trace, assertions)
        )

        assert result.passed
        assert result.pass_count == 4
        assert result.fail_count == 0
        for ar in result.assertion_results:
            assert ar.status == STATUS_PASS, f"{ar.assertion_id}: {ar.explanation}"

    def test_constraint_hard_fail(self, engine: AttestEngineFixture) -> None:
        """A constraint violation produces hard_fail."""
        trace = _refund_trace().build()
        assertions = [
            Assertion(
                assertion_id="e2e_cost_fail",
                type=TYPE_CONSTRAINT,
                spec={"field": "metadata.cost_usd", "operator": "lte", "value": 0.001},
            ),
        ]

        result = engine.evaluate(_chain_from_raw(trace, assertions))

        assert not result.passed
        assert result.fail_count == 1
        assert result.assertion_results[0].status == STATUS_HARD_FAIL

    def test_constraint_soft_fail(self, engine: AttestEngineFixture) -> None:
        """A soft constraint violation produces soft_fail (still counted as failure)."""
        trace = _refund_trace().build()
        assertions = [
            Assertion(
                assertion_id="e2e_cost_soft",
                type=TYPE_CONSTRAINT,
                spec={
                    "field": "metadata.cost_usd",
                    "operator": "lte",
                    "value": 0.001,
                    "soft": True,
                },
            ),
        ]

        result = engine.evaluate(_chain_from_raw(trace, assertions))

        assert len(result.soft_failures) == 1
        assert result.assertion_results[0].status == STATUS_SOFT_FAIL

    def test_schema_validation_fail(self, engine: AttestEngineFixture) -> None:
        """Schema mismatch produces hard_fail."""
        trace = _refund_trace().build()
        assertions = [
            Assertion(
                assertion_id="e2e_schema_fail",
                type=TYPE_SCHEMA,
                spec={
                    "target": "output.structured",
                    "schema": {
                        "type": "object",
                        "required": ["nonexistent_field"],
                    },
                },
            ),
        ]

        result = engine.evaluate(_chain_from_raw(trace, assertions))

        assert not result.passed
        assert result.assertion_results[0].status == STATUS_HARD_FAIL

    def test_trace_forbidden_tools(self, engine: AttestEngineFixture) -> None:
        """Forbidden tools check detects a called tool."""
        trace = _refund_trace().build()
        assertions = [
            Assertion(
                assertion_id="e2e_forbidden",
                type=TYPE_TRACE,
                spec={
                    "check": "forbidden_tools",
                    "tools": ["process_refund"],
                },
            ),
        ]

        result = engine.evaluate(_chain_from_raw(trace, assertions))

        assert not result.passed
        assert result.assertion_results[0].status == STATUS_HARD_FAIL

    def test_content_regex_match(self, engine: AttestEngineFixture) -> None:
        """Regex content check against output message."""
        trace = _refund_trace().build()
        assertions = [
            Assertion(
                assertion_id="e2e_regex",
                type=TYPE_CONTENT,
                spec={
                    "target": "output.message",
                    "check": "regex_match",
                    "value": r"RFD-\d+",
                },
            ),
        ]

        result = engine.evaluate(_chain_from_raw(trace, assertions))

        assert result.passed
        assert result.assertion_results[0].score == 1.0

    def test_mixed_pass_fail_batch(self, engine: AttestEngineFixture) -> None:
        """A batch with both passing and failing assertions."""
        trace = _refund_trace().build()
        assertions = [
            # Passes: output contains "refund"
            Assertion(
                assertion_id="e2e_mix_pass",
                type=TYPE_CONTENT,
                spec={"target": "output.message", "check": "contains", "value": "refund"},
            ),
            # Fails: cost absurdly low threshold
            Assertion(
                assertion_id="e2e_mix_fail",
                type=TYPE_CONSTRAINT,
                spec={"field": "metadata.cost_usd", "operator": "lte", "value": 0.0001},
            ),
        ]

        result = engine.evaluate(_chain_from_raw(trace, assertions))

        assert not result.passed
        assert result.pass_count == 1
        assert result.fail_count == 1

    def test_expect_dsl_round_trip(self, engine: AttestEngineFixture) -> None:
        """Verify the expect() DSL produces assertions the engine accepts."""
        trace = _refund_trace().build()

        # Build a "fake" result to feed into expect() — the DSL only needs the trace
        seed_result = AgentResult(trace=trace, assertion_results=[])

        chain = (
            expect(seed_result)
            .output_contains("refund")
            .cost_under(0.01)
            .tools_called_in_order(["lookup_order", "process_refund"])
            .output_matches_regex(r"\$\d+\.\d{2}")
        )

        result = engine.evaluate(chain)

        assert result.passed
        assert result.pass_count == 4

    def test_step_count_constraint(self, engine: AttestEngineFixture) -> None:
        """Verify step count constraint against actual trace steps."""
        trace = _refund_trace().build()
        assertions = [
            Assertion(
                assertion_id="e2e_step_count",
                type=TYPE_CONSTRAINT,
                spec={"field": "steps.length", "operator": "eq", "value": 3},
            ),
        ]

        result = engine.evaluate(_chain_from_raw(trace, assertions))

        assert result.passed

    def test_tool_call_count_constraint(self, engine: AttestEngineFixture) -> None:
        """Verify tool_call count filtering works end-to-end."""
        trace = _refund_trace().build()
        assertions = [
            Assertion(
                assertion_id="e2e_tool_count",
                type=TYPE_CONSTRAINT,
                spec={
                    "field": "steps[?type=='tool_call'].length",
                    "operator": "eq",
                    "value": 2,
                },
            ),
        ]

        result = engine.evaluate(_chain_from_raw(trace, assertions))

        assert result.passed


class _FakeChain:
    """Minimal stand-in providing .trace and .assertions for engine.evaluate()."""

    def __init__(self, trace: object, assertions: list[Assertion]) -> None:
        self.trace = trace
        self.assertions = assertions


def _chain_from_raw(trace: object, assertions: list[Assertion]) -> _FakeChain:  # type: ignore[return]
    """Create a minimal chain-like object for engine.evaluate()."""
    return _FakeChain(trace, assertions)
