"""Full customer service agent — demonstrates a complete Attest-tested agent."""

from __future__ import annotations

from typing import Any

from attest.agent import agent
from attest.delegate import delegate
from attest.trace import TraceBuilder


@agent("customer-service")
def customer_service(builder: TraceBuilder, user_message: str) -> dict[str, Any]:
    """Multi-step customer service agent with routing and delegation.

    Flow: classify intent -> route to specialist -> execute action -> respond
    """
    # Step 1: Classify intent
    builder.add_llm_call(
        name="gpt-4.1-mini",
        args={
            "model": "gpt-4.1-mini",
            "messages": [
                {"role": "system", "content": "Classify: refund, status, complaint, general"},
                {"role": "user", "content": user_message},
            ],
        },
        result={"completion": "refund", "confidence": 0.95},
    )

    # Step 2: Look up customer context
    builder.add_tool_call(
        name="lookup_customer",
        args={"query": user_message},
        result={
            "customer_id": "CUST-001",
            "name": "Jane Smith",
            "tier": "gold",
            "recent_orders": [{"id": "ORD-5678", "amount": 129.99, "status": "delivered"}],
        },
    )

    # Step 3: Delegate to refund specialist
    with delegate("refund-specialist") as specialist:
        specialist.set_input(
            order_id="ORD-5678",
            customer_tier="gold",
            reason=user_message,
        )
        specialist.add_tool_call(
            name="check_refund_eligibility",
            args={"order_id": "ORD-5678", "days_since_delivery": 5},
            result={"eligible": True, "max_amount": 129.99},
        )
        specialist.add_tool_call(
            name="process_refund",
            args={"order_id": "ORD-5678", "amount": 129.99, "reason": "customer_request"},
            result={"refund_id": "RFD-999", "status": "approved", "eta_days": 3},
        )
        specialist.set_output(
            message="Refund of $129.99 approved. Refund ID: RFD-999. ETA: 3 business days.",
        )
        specialist.set_metadata(total_tokens=80, cost_usd=0.002, latency_ms=800)

    # Step 4: Generate final response
    builder.add_llm_call(
        name="gpt-4.1",
        args={
            "model": "gpt-4.1",
            "messages": [{"role": "user", "content": "Compose final response with refund details"}],
        },
        result={
            "completion": (
                "Hi Jane, I've processed your refund of $129.99 for order ORD-5678. "
                "Your refund ID is RFD-999 and you should see the funds within 3 business days. "
                "As a valued Gold member, your refund has been prioritized."
            ),
        },
    )

    builder.set_metadata(total_tokens=200, cost_usd=0.005, latency_ms=1500, model="gpt-4.1")

    return {
        "message": (
            "Hi Jane, I've processed your refund of $129.99 for order ORD-5678. "
            "Your refund ID is RFD-999 and you should see the funds within 3 business days. "
            "As a valued Gold member, your refund has been prioritized."
        ),
        "structured": {
            "intent": "refund",
            "refund_id": "RFD-999",
            "amount": 129.99,
            "status": "approved",
        },
    }
