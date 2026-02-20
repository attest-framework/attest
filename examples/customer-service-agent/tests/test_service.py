"""Comprehensive tests for the customer service agent.

Demonstrates all 8 assertion layers in a realistic scenario.
"""

from __future__ import annotations

import sys
from pathlib import Path

# Add agent directory to path for imports
sys.path.insert(0, str(Path(__file__).parent.parent / "agent"))

from main import customer_service  # noqa: E402
from attest.expect import expect  # noqa: E402


def test_refund_flow_layers_1_through_4(attest) -> None:
    """Layers 1-4: Schema, constraints, trace, content."""
    result = customer_service(user_message="I want a refund for my recent order")

    chain = (
        expect(result)
        # L1: Schema — verify structured output shape
        .output_matches_schema({
            "type": "object",
            "properties": {
                "intent": {"type": "string"},
                "refund_id": {"type": "string"},
                "amount": {"type": "number"},
                "status": {"type": "string"},
            },
            "required": ["intent", "refund_id", "status"],
        })
        # L2: Constraints — cost and performance
        .cost_under(0.05)
        .latency_under(5000)
        .tokens_under(500)
        # L3: Trace — tool ordering
        .tools_called_in_order(["lookup_customer", "check_refund_eligibility", "process_refund"])
        .required_tools(["lookup_customer"])
        .forbidden_tools(["delete_customer", "admin_override"])
        # L4: Content — output quality
        .output_contains("refund")
        .output_contains("RFD-999")
        .output_not_contains("error")
        .output_not_contains("denied")
    )

    attest.evaluate(chain)


def test_refund_flow_layer_5_semantic(attest) -> None:
    """Layer 5: Embedding similarity."""
    result = customer_service(user_message="I want a refund for my recent order")

    chain = (
        expect(result)
        # L5: Response should be semantically similar to a refund confirmation
        .output_similar_to(
            "Your refund has been processed and you will receive the money back soon.",
            threshold=0.7,
        )
    )

    attest.evaluate(chain)


def test_refund_flow_layer_6_judge(attest) -> None:
    """Layer 6: LLM-as-judge for subjective quality."""
    result = customer_service(user_message="I want a refund for my recent order")

    chain = (
        expect(result)
        # L6: Judge the response quality
        .passes_judge(
            "Does the response acknowledge the customer by name and provide specific refund details?",
            threshold=0.8,
        )
        .passes_judge(
            "Is the response professional and empathetic?",
            threshold=0.7,
        )
    )

    attest.evaluate(chain)


def test_refund_flow_layers_7_8_multi_agent(attest) -> None:
    """Layers 7-8: Simulation and multi-agent trace tree."""
    result = customer_service(user_message="I want a refund for my recent order")

    chain = (
        expect(result)
        # L8: Multi-agent assertions
        .agent_called("refund-specialist")
        .delegation_depth(2)
        .follows_transitions([("customer-service", "refund-specialist")])
        .aggregate_cost_under(0.10)
        .aggregate_tokens_under(1000)
        # Verify specialist produced useful output
        .agent_output_contains("refund-specialist", "Refund")
    )

    attest.evaluate(chain)


def test_all_keywords_present(attest) -> None:
    """Verify all required keywords appear in the response."""
    result = customer_service(user_message="I want a refund for my recent order")

    chain = (
        expect(result)
        .output_has_all_keywords(["refund", "RFD-999", "business days"])
        .output_forbids(["sorry for the inconvenience", "please hold"])
    )

    attest.evaluate(chain)
