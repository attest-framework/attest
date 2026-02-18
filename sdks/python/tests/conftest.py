"""Shared test fixtures for Attest SDK tests."""

from __future__ import annotations

import pytest

from attest._proto.types import (
    Assertion,
    AssertionResult,
    Step,
    Trace,
    TraceMetadata,
    STATUS_PASS,
    STATUS_HARD_FAIL,
    STATUS_SOFT_FAIL,
    TYPE_SCHEMA,
    TYPE_CONSTRAINT,
    TYPE_TRACE,
    TYPE_CONTENT,
)
from attest.result import AgentResult
from attest.trace import TraceBuilder


@pytest.fixture
def sample_trace() -> Trace:
    """A sample refund agent trace for testing."""
    return (
        TraceBuilder(agent_id="refund-agent")
        .set_trace_id("trc_test_refund")
        .set_input(user_message="I want a refund for order ORD-123")
        .add_llm_call(
            "reasoning",
            args={"model": "gpt-4.1"},
            result={"completion": "I need to look up the order."},
            metadata={"duration_ms": 1200},
        )
        .add_tool_call(
            "lookup_order",
            args={"order_id": "ORD-123"},
            result={"status": "delivered", "amount": 89.99, "eligible_for_refund": True},
            metadata={"duration_ms": 45},
        )
        .add_tool_call(
            "process_refund",
            args={"order_id": "ORD-123", "amount": 89.99},
            result={"refund_id": "RFD-001", "estimated_days": 3},
            metadata={"duration_ms": 120},
        )
        .set_output(
            message="Your refund of $89.99 has been processed. You'll see it in 3 business days. Refund ID: RFD-001.",
            structured={"refund_id": "RFD-001", "confidence": 0.95},
        )
        .set_metadata(
            total_tokens=1350,
            cost_usd=0.0067,
            latency_ms=4200,
            model="gpt-4.1",
        )
        .build()
    )


@pytest.fixture
def sample_result(sample_trace: Trace) -> AgentResult:
    """A sample AgentResult with passing assertions."""
    return AgentResult(
        trace=sample_trace,
        assertion_results=[
            AssertionResult(
                assertion_id="a1",
                status=STATUS_PASS,
                score=1.0,
                explanation="Schema validation passed",
            ),
            AssertionResult(
                assertion_id="a2",
                status=STATUS_PASS,
                score=1.0,
                explanation="Cost under budget",
            ),
        ],
    )


@pytest.fixture
def sample_assertions() -> list[Assertion]:
    """A sample set of L1-L4 assertions."""
    return [
        Assertion(
            assertion_id="schema_1",
            type=TYPE_SCHEMA,
            spec={
                "target": "steps[?name=='lookup_order'].result",
                "schema": {
                    "type": "object",
                    "required": ["status", "amount"],
                    "properties": {
                        "status": {"type": "string"},
                        "amount": {"type": "number"},
                    },
                },
            },
        ),
        Assertion(
            assertion_id="constraint_1",
            type=TYPE_CONSTRAINT,
            spec={
                "field": "metadata.cost_usd",
                "operator": "lte",
                "value": 0.01,
            },
        ),
        Assertion(
            assertion_id="trace_1",
            type=TYPE_TRACE,
            spec={
                "check": "contains_in_order",
                "tools": ["lookup_order", "process_refund"],
            },
        ),
        Assertion(
            assertion_id="content_1",
            type=TYPE_CONTENT,
            spec={
                "target": "output.message",
                "check": "contains",
                "value": "refund",
            },
        ),
    ]
