from attest import agent, expect
from attest.plugin import AttestEngineFixture


@agent("refund-agent")
def refund_agent(builder, order_id):
    builder.add_tool_call(
        "lookup_order",
        args={"order_id": order_id},
        result={"status": "delivered", "amount": 89.99},
    )
    builder.add_tool_call(
        "process_refund", args={"order_id": order_id}, result={"refund_id": "RFD-001"}
    )
    builder.set_metadata(cost_usd=0.005, total_tokens=150, latency_ms=1200)
    return {
        "message": "Your refund of $89.99 has been processed.",
        "structured": {"refund_id": "RFD-001"},
    }


def test_refund(attest: AttestEngineFixture) -> None:
    result = refund_agent(order_id="ORD-123456")

    chain = (
        expect(result)
        # Layer 1 — schema (free, instant)
        .output_matches_schema({"type": "object", "required": ["refund_id"]})
        # Layer 2 — constraints (free, instant)
        .cost_under(0.05)
        # Layer 3 — trace (free, instant)
        .tools_called_in_order(["lookup_order", "process_refund"])
        # Layer 4 — content (free, instant)
        .output_contains("refund")
        .output_not_contains("sorry")
    )

    attest.evaluate(chain)
