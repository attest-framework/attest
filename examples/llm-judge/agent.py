"""Customer support agent — demonstrates Layer 6 (LLM-as-Judge) assertions."""

from __future__ import annotations

from typing import Any

from attest.agent import agent
from attest.trace import TraceBuilder


@agent("support-agent")
def handle_complaint(builder: TraceBuilder, complaint: str) -> dict[str, Any]:
    """Handle a customer complaint with empathy and resolution."""
    builder.add_llm_call(
        name="gpt-4.1",
        args={
            "model": "gpt-4.1",
            "messages": [
                {
                    "role": "system",
                    "content": (
                        "You are a customer support agent. Respond with empathy, "
                        "acknowledge the issue, and provide a concrete resolution."
                    ),
                },
                {"role": "user", "content": complaint},
            ],
        },
        result={
            "completion": (
                "I'm sorry to hear about this issue with your order. I understand "
                "how frustrating that must be. I've initiated a full refund of $49.99 "
                "to your original payment method. You should see it within 3-5 business "
                "days. Is there anything else I can help you with?"
            ),
        },
    )

    builder.add_tool_call(
        name="issue_refund",
        args={"order_id": "ORD-9876", "amount": 49.99},
        result={"refund_id": "RFD-555", "status": "approved"},
    )

    builder.set_metadata(total_tokens=180, cost_usd=0.004, latency_ms=1500, model="gpt-4.1")

    return {
        "message": (
            "I'm sorry to hear about this issue with your order. I understand "
            "how frustrating that must be. I've initiated a full refund of $49.99 "
            "to your original payment method. You should see it within 3-5 business "
            "days. Is there anything else I can help you with?"
        ),
        "structured": {"refund_id": "RFD-555", "amount": 49.99, "status": "approved"},
    }


@agent("content-moderator")
def moderate_content(builder: TraceBuilder, content: str) -> dict[str, Any]:
    """Moderate user-generated content for policy compliance."""
    builder.add_llm_call(
        name="gpt-4.1-mini",
        args={
            "model": "gpt-4.1-mini",
            "messages": [
                {
                    "role": "system",
                    "content": "Review the content for policy violations. Be fair and unbiased.",
                },
                {"role": "user", "content": content},
            ],
        },
        result={
            "completion": (
                "This content is within community guidelines. The post discusses "
                "a product review with constructive criticism. No violations detected."
            ),
        },
    )

    builder.set_metadata(total_tokens=90, cost_usd=0.001, latency_ms=500, model="gpt-4.1-mini")

    return {
        "message": (
            "This content is within community guidelines. The post discusses "
            "a product review with constructive criticism. No violations detected."
        ),
        "structured": {"decision": "approved", "violations": []},
    }
