"""Tests for expect() DSL."""

from __future__ import annotations

from attest._proto.types import Step, Trace, TraceMetadata
from attest.expect import expect
from attest.result import AgentResult


def _make_result() -> AgentResult:
    trace = Trace(
        trace_id="trc_test",
        output={
            "message": "Your refund of $89.99 has been processed.",
            "structured": {"refund_id": "RFD-001"},
        },
        steps=[
            Step(
                type="tool_call",
                name="lookup_order",
                args={"order_id": "ORD-123"},
                result={"status": "delivered"},
            ),
            Step(
                type="tool_call",
                name="process_refund",
                args={"order_id": "ORD-123"},
                result={"refund_id": "RFD-001"},
            ),
        ],
        metadata=TraceMetadata(cost_usd=0.005, latency_ms=2000, total_tokens=500),
    )
    return AgentResult(trace=trace)


def test_expect_output_contains() -> None:
    chain = expect(_make_result()).output_contains("refund")
    assert len(chain.assertions) == 1
    assert chain.assertions[0].type == "content"
    assert chain.assertions[0].spec["check"] == "contains"


def test_expect_chaining() -> None:
    chain = (
        expect(_make_result())
        .output_contains("refund")
        .cost_under(0.01)
        .tools_called_in_order(["lookup_order", "process_refund"])
    )
    assert len(chain.assertions) == 3
    types = [a.type for a in chain.assertions]
    assert "content" in types
    assert "constraint" in types
    assert "trace" in types


def test_expect_schema() -> None:
    schema = {"type": "object", "required": ["refund_id"]}
    chain = expect(_make_result()).output_matches_schema(schema)
    assert chain.assertions[0].type == "schema"
    assert chain.assertions[0].spec["target"] == "output.structured"


def test_expect_constraint_between() -> None:
    chain = expect(_make_result()).tokens_between(100, 1000)
    a = chain.assertions[0]
    assert a.spec["operator"] == "between"
    assert a.spec["min"] == 100
    assert a.spec["max"] == 1000


def test_expect_trace_checks() -> None:
    chain = (
        expect(_make_result())
        .tools_called_in_order(["lookup_order", "process_refund"])
        .no_duplicate_tools()
        .required_tools(["lookup_order"])
        .forbidden_tools(["delete_order"])
    )
    assert len(chain.assertions) == 4


def test_expect_content_checks() -> None:
    chain = (
        expect(_make_result())
        .output_contains("refund")
        .output_not_contains("error")
        .output_matches_regex(r"RFD-\d+")
        .output_has_all_keywords(["refund", "processed"])
        .output_forbids(["kill", "harm"])
    )
    assert len(chain.assertions) == 5


def test_expect_soft_flag() -> None:
    chain = expect(_make_result()).cost_under(0.01, soft=True)
    assert chain.assertions[0].spec["soft"] is True


def test_expect_tool_schema() -> None:
    schema = {"type": "object", "required": ["order_id"]}
    chain = expect(_make_result()).tool_args_match_schema("lookup_order", schema)
    assert chain.assertions[0].spec["target"] == "steps[?name=='lookup_order'].args"
