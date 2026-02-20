"""Tests for Layer 5 (Embedding) and Layer 6 (LLM Judge) DSL methods."""

from __future__ import annotations

import pytest

from attest._proto.types import TYPE_EMBEDDING, TYPE_LLM_JUDGE
from attest.expect import expect
from attest.result import AgentResult


class TestOutputSimilarTo:
    """Tests for ExpectChain.output_similar_to()."""

    def test_adds_embedding_assertion(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to("Paris is the capital of France.")
        assertions = chain.assertions
        assert len(assertions) == 1
        assert assertions[0].type == TYPE_EMBEDDING

    def test_spec_target(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to("some reference")
        spec = chain.assertions[0].spec
        assert spec["target"] == "output.message"

    def test_spec_reference(self, embedding_result: AgentResult) -> None:
        ref = "The capital of France is Paris."
        chain = expect(embedding_result).output_similar_to(ref)
        assert chain.assertions[0].spec["reference"] == ref

    def test_default_threshold(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to("reference text")
        assert chain.assertions[0].spec["threshold"] == 0.8

    def test_custom_threshold(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to("reference text", threshold=0.95)
        assert chain.assertions[0].spec["threshold"] == 0.95

    def test_default_model_is_none(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to("reference text")
        assert chain.assertions[0].spec["model"] is None

    def test_custom_model(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to(
            "reference text", model="text-embedding-3-small"
        )
        assert chain.assertions[0].spec["model"] == "text-embedding-3-small"

    def test_default_soft_false(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to("reference text")
        assert chain.assertions[0].spec["soft"] is False

    def test_soft_true(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to("reference text", soft=True)
        assert chain.assertions[0].spec["soft"] is True

    def test_chaining(self, embedding_result: AgentResult) -> None:
        chain = (
            expect(embedding_result)
            .output_similar_to("reference A")
            .output_similar_to("reference B", threshold=0.9)
        )
        assertions = chain.assertions
        assert len(assertions) == 2
        assert assertions[0].spec["reference"] == "reference A"
        assert assertions[1].spec["reference"] == "reference B"
        assert assertions[1].spec["threshold"] == 0.9

    def test_assertion_id_is_unique(self, embedding_result: AgentResult) -> None:
        chain = (
            expect(embedding_result)
            .output_similar_to("ref A")
            .output_similar_to("ref B")
        )
        ids = [a.assertion_id for a in chain.assertions]
        assert ids[0] != ids[1]

    def test_combined_with_other_layers(self, embedding_result: AgentResult) -> None:
        chain = (
            expect(embedding_result)
            .output_contains("Paris")
            .output_similar_to("Paris is the capital.", threshold=0.85)
            .cost_under(0.01)
        )
        assert len(chain.assertions) == 3
        assert chain.assertions[1].type == TYPE_EMBEDDING


class TestPassesJudge:
    """Tests for ExpectChain.passes_judge()."""

    def test_adds_llm_judge_assertion(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("Response is accurate and clear.")
        assertions = chain.assertions
        assert len(assertions) == 1
        assert assertions[0].type == TYPE_LLM_JUDGE

    def test_spec_target(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("some criteria")
        assert chain.assertions[0].spec["target"] == "output.message"

    def test_spec_criteria(self, judge_result: AgentResult) -> None:
        criteria = "Response must be factually correct about transformers."
        chain = expect(judge_result).passes_judge(criteria)
        assert chain.assertions[0].spec["criteria"] == criteria

    def test_default_rubric(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("some criteria")
        assert chain.assertions[0].spec["rubric"] == "default"

    def test_custom_rubric(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("some criteria", rubric="strict")
        assert chain.assertions[0].spec["rubric"] == "strict"

    def test_default_threshold(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("some criteria")
        assert chain.assertions[0].spec["threshold"] == 0.8

    def test_custom_threshold(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("some criteria", threshold=0.9)
        assert chain.assertions[0].spec["threshold"] == 0.9

    def test_default_model_is_none(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("some criteria")
        assert chain.assertions[0].spec["model"] is None

    def test_custom_model(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("some criteria", model="gpt-4.1")
        assert chain.assertions[0].spec["model"] == "gpt-4.1"

    def test_default_soft_false(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("some criteria")
        assert chain.assertions[0].spec["soft"] is False

    def test_soft_true(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("some criteria", soft=True)
        assert chain.assertions[0].spec["soft"] is True

    def test_chaining_multiple_judges(self, judge_result: AgentResult) -> None:
        chain = (
            expect(judge_result)
            .passes_judge("Response is accurate.")
            .passes_judge("Response is concise.", rubric="length", threshold=0.7)
        )
        assertions = chain.assertions
        assert len(assertions) == 2
        assert assertions[0].spec["criteria"] == "Response is accurate."
        assert assertions[1].spec["rubric"] == "length"
        assert assertions[1].spec["threshold"] == 0.7

    def test_assertion_id_is_unique(self, judge_result: AgentResult) -> None:
        chain = (
            expect(judge_result)
            .passes_judge("criteria A")
            .passes_judge("criteria B")
        )
        ids = [a.assertion_id for a in chain.assertions]
        assert ids[0] != ids[1]

    def test_combined_l5_l6(self, judge_result: AgentResult) -> None:
        chain = (
            expect(judge_result)
            .output_similar_to("Transformers use attention mechanisms.", threshold=0.85)
            .passes_judge("Explanation is technically accurate.")
        )
        assert len(chain.assertions) == 2
        assert chain.assertions[0].type == TYPE_EMBEDDING
        assert chain.assertions[1].type == TYPE_LLM_JUDGE


class TestDynamicThreshold:
    """Tests for dynamic threshold support in output_similar_to and passes_judge."""

    def test_dynamic_threshold_embedding(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to("ref", threshold="dynamic")
        assert chain.assertions[0].spec["threshold"] == "dynamic"

    def test_dynamic_threshold_judge(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("criteria", threshold="dynamic")
        assert chain.assertions[0].spec["threshold"] == "dynamic"

    def test_float_threshold_still_works_embedding(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to("ref", threshold=0.75)
        assert chain.assertions[0].spec["threshold"] == 0.75

    def test_float_threshold_still_works_judge(self, judge_result: AgentResult) -> None:
        chain = expect(judge_result).passes_judge("criteria", threshold=0.6)
        assert chain.assertions[0].spec["threshold"] == 0.6

    def test_default_threshold_unchanged(self, embedding_result: AgentResult) -> None:
        chain = expect(embedding_result).output_similar_to("ref")
        assert chain.assertions[0].spec["threshold"] == 0.8
