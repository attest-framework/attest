from attest import AgentResult, TraceBuilder, expect


def refund_agent_result() -> AgentResult:
    """Stand-in for your agent: build the trace it produced."""
    trace = (
        TraceBuilder("refund-agent")
        .add_tool_call("lookup_order", args={"order_id": "ORD-123"}, result={"status": "paid"})
        .add_tool_call("process_refund", args={"order_id": "ORD-123"}, result={"refund_id": "R-1"})
        .set_output(message="Your refund R-1 is on its way.", structured={"refund_id": "R-1"})
        .set_metadata(cost_usd=0.01, latency_ms=800)
        .build()
    )
    return AgentResult(trace=trace)


def test_refund_agent(attest):
    result = refund_agent_result()
    attest.evaluate(
        expect(result)
        # Layer 1: schema validation
        .output_matches_schema({"type": "object", "required": ["refund_id"]})
        # Layer 2: cost and latency constraints
        .cost_under(0.05)
        .latency_under(5000)
        # Layer 3: trace structure
        .tools_called_in_order(["lookup_order", "process_refund"])
        .no_tool_loops("lookup_order", max_repetitions=3)
        # Layer 4: content
        .output_contains("refund")
        .output_not_contains("sorry")
    )
