"""Tests for the RAG chatbot — demonstrates retrieval-specific assertions."""

from __future__ import annotations

from agent import rag_chat
from attest.expect import expect


def test_rag_retrieves_and_answers(attest) -> None:
    """Verify the RAG pipeline: embed -> retrieve -> generate."""
    result = rag_chat(question="What is Attest?")

    chain = (
        expect(result)
        # L1: Schema — structured output has required fields
        .output_matches_schema({
            "type": "object",
            "properties": {
                "answer": {"type": "string"},
                "sources": {"type": "array"},
                "confidence": {"type": "number"},
            },
            "required": ["answer", "sources"],
        })
        # L2: Cost and latency
        .cost_under(0.05)
        .latency_under(5000)
        # L3: Trace — verify retrieval step exists
        .tools_called_in_order(["vector_search"])
        .required_tools(["vector_search"])
        # L4: Content — answer should reference the framework
        .output_contains("Attest")
        .output_contains("8-layer")
    )

    attest.evaluate(chain)


def test_rag_answer_grounded_in_context(attest) -> None:
    """Verify the answer is semantically grounded in retrieved context."""
    result = rag_chat(question="How do I install Attest?")

    chain = (
        expect(result)
        # L4: Content — should mention installation methods
        .output_has_any_keyword(["pip install", "npm install", "attest-ai"])
        # L5: Semantic similarity to expected answer
        .output_similar_to(
            "You can install Attest using pip or npm package managers.",
            threshold=0.7,
        )
        # L6: Judge — is the answer factually grounded?
        .passes_judge(
            "Does the response only contain information from the retrieved context, "
            "without hallucinating additional claims?",
            threshold=0.8,
        )
    )

    attest.evaluate(chain)


def test_rag_no_hallucination(attest) -> None:
    """Verify the chatbot doesn't hallucinate information."""
    result = rag_chat(question="What are Attest assertion layers?")

    chain = (
        expect(result)
        # L4: Should mention known layers
        .output_has_any_keyword(["Schema", "Constraints", "Trace", "Content"])
        # L4: Should not contain fabricated claims
        .output_forbids(["version 5.0", "acquired by Google", "deprecated"])
        # L6: Judge for hallucination
        .passes_judge(
            "Does the response stay factual and avoid inventing features or "
            "capabilities not mentioned in the documentation?",
            threshold=0.8,
        )
    )

    attest.evaluate(chain)


def test_rag_trace_structure(attest) -> None:
    """Verify the RAG trace has the expected 3-step structure."""
    result = rag_chat(question="What is Attest?")

    chain = (
        expect(result)
        # Embedding -> Retrieval -> Generation = 3 steps
        .step_count("eq", 3)
        # No duplicate tools (each step is different)
        .no_duplicate_tools()
    )

    attest.evaluate(chain)
