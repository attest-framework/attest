"""Tests demonstrating Layer 6 — LLM-as-Judge assertions.

Layer 6 uses a separate LLM to evaluate agent output against
human-defined criteria. The judge scores on [0, 1] and passes
if above the threshold. Useful for subjective quality checks
that can't be captured by regex or keyword matching.
"""

from __future__ import annotations

from agent import handle_complaint, moderate_content
from attest.expect import expect


def test_complaint_response_quality(attest) -> None:
    """Judge the complaint response for empathy, resolution, and professionalism."""
    result = handle_complaint(complaint="My order arrived damaged and I want a refund.")

    chain = (
        expect(result)
        # Layer 6: LLM judges — each checks a different quality dimension
        .passes_judge(
            "Does the response show genuine empathy and acknowledge the customer's frustration?",
            threshold=0.8,
        )
        .passes_judge(
            "Does the response provide a concrete resolution (refund, replacement, etc.)?",
            threshold=0.8,
        )
        .passes_judge(
            "Is the tone professional and appropriate for customer support?",
            rubric="professional_tone",
            threshold=0.9,
        )
        # Layer 4: Content checks complement the judge
        .output_contains("refund")
        .output_not_contains("your fault")
        # Layer 2: Cost constraint
        .cost_under(0.05)
    )

    attest.evaluate(chain)


def test_judge_with_custom_rubric(attest) -> None:
    """Demonstrate custom rubric usage with the LLM judge."""
    result = handle_complaint(complaint="Your product is terrible!")

    chain = (
        expect(result)
        .passes_judge(
            "Does the agent de-escalate the situation rather than matching the customer's tone?",
            rubric=(
                "Score 1.0 if the response is calm, empathetic, and offers resolution. "
                "Score 0.5 if the response is neutral but doesn't address emotions. "
                "Score 0.0 if the response is defensive or argumentative."
            ),
            threshold=0.7,
        )
    )

    attest.evaluate(chain)


def test_content_moderation_fairness(attest) -> None:
    """Judge content moderation decisions for fairness and reasoning."""
    result = moderate_content(
        content="This product is overpriced and the quality is disappointing."
    )

    chain = (
        expect(result)
        # Layer 6: Judge fairness of moderation decisions
        .passes_judge(
            "Is the moderation decision fair? Constructive criticism should be allowed.",
            threshold=0.8,
        )
        .passes_judge(
            "Does the explanation provide clear reasoning for the decision?",
            threshold=0.7,
        )
        # Layer 4: Content
        .output_contains("guidelines")
        .output_not_contains("removed")
    )

    attest.evaluate(chain)


def test_judge_soft_failure(attest) -> None:
    """Demonstrate soft failure mode for LLM judge assertions."""
    result = handle_complaint(complaint="I need help with my order.")

    chain = (
        expect(result)
        # Hard judge — must pass
        .passes_judge("Does the response address the customer's concern?", threshold=0.7)
        # Soft judge — warns but doesn't fail the test
        .passes_judge(
            "Does the response proactively offer additional assistance?",
            threshold=0.9,
            soft=True,
        )
    )

    attest.evaluate(chain)
