"""Fluent assertion DSL for Attest agent tests."""

from __future__ import annotations

import uuid
from typing import Any

from attest._proto.types import (
    TYPE_CONSTRAINT,
    TYPE_CONTENT,
    TYPE_EMBEDDING,
    TYPE_LLM_JUDGE,
    TYPE_SCHEMA,
    TYPE_TRACE,
    TYPE_TRACE_TREE,
    Assertion,
    Trace,
)
from attest.result import AgentResult


class ExpectChain:
    """Fluent assertion builder. Collects assertions for batch evaluation."""

    def __init__(self, result: AgentResult) -> None:
        self._result = result
        self._assertions: list[Assertion] = []

    @property
    def assertions(self) -> list[Assertion]:
        """Return collected assertions."""
        return list(self._assertions)

    @property
    def trace(self) -> Trace:
        """Return the trace from the result."""
        return self._result.trace

    def _add(self, assertion_type: str, spec: dict[str, Any]) -> ExpectChain:
        """Add an assertion to the collection."""
        self._assertions.append(
            Assertion(
                assertion_id=f"assert_{uuid.uuid4().hex[:8]}",
                type=assertion_type,
                spec=spec,
            )
        )
        return self

    # ── Layer 1: Schema ──

    def output_matches_schema(self, schema: dict[str, Any]) -> ExpectChain:
        """Assert that output.structured matches the given JSON Schema."""
        return self._add(TYPE_SCHEMA, {"target": "output.structured", "schema": schema})

    def output_field_matches_schema(self, field: str, schema: dict[str, Any]) -> ExpectChain:
        """Assert that a specific output field matches schema."""
        return self._add(TYPE_SCHEMA, {"target": f"output.{field}", "schema": schema})

    def tool_args_match_schema(self, tool_name: str, schema: dict[str, Any]) -> ExpectChain:
        """Assert that a tool's args match the given JSON Schema."""
        return self._add(
            TYPE_SCHEMA,
            {
                "target": f"steps[?name=='{tool_name}'].args",
                "schema": schema,
            },
        )

    def tool_result_matches_schema(self, tool_name: str, schema: dict[str, Any]) -> ExpectChain:
        """Assert that a tool's result matches the given JSON Schema."""
        return self._add(
            TYPE_SCHEMA,
            {
                "target": f"steps[?name=='{tool_name}'].result",
                "schema": schema,
            },
        )

    # ── Layer 2: Constraints ──

    def cost_under(self, max_cost: float, *, soft: bool = False) -> ExpectChain:
        """Assert total cost is under a threshold."""
        return self._add(
            TYPE_CONSTRAINT,
            {
                "field": "metadata.cost_usd",
                "operator": "lte",
                "value": max_cost,
                "soft": soft,
            },
        )

    def latency_under(self, max_ms: int, *, soft: bool = False) -> ExpectChain:
        """Assert total latency is under a threshold (ms)."""
        return self._add(
            TYPE_CONSTRAINT,
            {
                "field": "metadata.latency_ms",
                "operator": "lte",
                "value": max_ms,
                "soft": soft,
            },
        )

    def tokens_under(self, max_tokens: int, *, soft: bool = False) -> ExpectChain:
        """Assert total tokens is under a threshold."""
        return self._add(
            TYPE_CONSTRAINT,
            {
                "field": "metadata.total_tokens",
                "operator": "lte",
                "value": max_tokens,
                "soft": soft,
            },
        )

    def tokens_between(
        self, min_tokens: int, max_tokens: int, *, soft: bool = False
    ) -> ExpectChain:
        """Assert total tokens is between min and max."""
        return self._add(
            TYPE_CONSTRAINT,
            {
                "field": "metadata.total_tokens",
                "operator": "between",
                "min": min_tokens,
                "max": max_tokens,
                "soft": soft,
            },
        )

    def step_count(self, operator: str, value: int, *, soft: bool = False) -> ExpectChain:
        """Assert step count with given operator."""
        return self._add(
            TYPE_CONSTRAINT,
            {
                "field": "steps.length",
                "operator": operator,
                "value": value,
                "soft": soft,
            },
        )

    def tool_call_count(self, operator: str, value: int, *, soft: bool = False) -> ExpectChain:
        """Assert tool call count with given operator."""
        return self._add(
            TYPE_CONSTRAINT,
            {
                "field": "steps[?type=='tool_call'].length",
                "operator": operator,
                "value": value,
                "soft": soft,
            },
        )

    def constraint(
        self,
        field: str,
        operator: str,
        value: float | None = None,
        *,
        min: float | None = None,
        max: float | None = None,
        soft: bool = False,
    ) -> ExpectChain:
        """Generic constraint assertion."""
        spec: dict[str, Any] = {"field": field, "operator": operator, "soft": soft}
        if value is not None:
            spec["value"] = value
        if min is not None:
            spec["min"] = min
        if max is not None:
            spec["max"] = max
        return self._add(TYPE_CONSTRAINT, spec)

    # ── Layer 3: Trace ──

    def tools_called_in_order(self, tools: list[str], *, soft: bool = False) -> ExpectChain:
        """Assert tools were called in the given order (non-contiguous)."""
        return self._add(
            TYPE_TRACE,
            {
                "check": "contains_in_order",
                "tools": tools,
                "soft": soft,
            },
        )

    def tools_called_exactly(self, tools: list[str], *, soft: bool = False) -> ExpectChain:
        """Assert tools were called in exact contiguous order."""
        return self._add(
            TYPE_TRACE,
            {
                "check": "exact_order",
                "tools": tools,
                "soft": soft,
            },
        )

    def no_tool_loops(
        self, tool: str, max_repetitions: int = 1, *, soft: bool = False
    ) -> ExpectChain:
        """Assert a tool is not called more than max_repetitions times."""
        return self._add(
            TYPE_TRACE,
            {
                "check": "loop_detection",
                "tool": tool,
                "max_repetitions": max_repetitions,
                "soft": soft,
            },
        )

    def no_duplicate_tools(self, *, soft: bool = False) -> ExpectChain:
        """Assert no tool is called more than once."""
        return self._add(TYPE_TRACE, {"check": "no_duplicates", "soft": soft})

    def required_tools(self, tools: list[str], *, soft: bool = False) -> ExpectChain:
        """Assert all listed tools were called."""
        return self._add(
            TYPE_TRACE,
            {
                "check": "required_tools",
                "tools": tools,
                "soft": soft,
            },
        )

    def forbidden_tools(self, tools: list[str], *, soft: bool = False) -> ExpectChain:
        """Assert none of the listed tools were called."""
        return self._add(
            TYPE_TRACE,
            {
                "check": "forbidden_tools",
                "tools": tools,
                "soft": soft,
            },
        )

    # ── Layer 4: Content ──

    def output_contains(
        self, value: str, *, case_sensitive: bool = False, soft: bool = False
    ) -> ExpectChain:
        """Assert output message contains a string."""
        return self._add(
            TYPE_CONTENT,
            {
                "target": "output.message",
                "check": "contains",
                "value": value,
                "case_sensitive": case_sensitive,
                "soft": soft,
            },
        )

    def output_not_contains(
        self, value: str, *, case_sensitive: bool = False, soft: bool = False
    ) -> ExpectChain:
        """Assert output message does not contain a string."""
        return self._add(
            TYPE_CONTENT,
            {
                "target": "output.message",
                "check": "not_contains",
                "value": value,
                "case_sensitive": case_sensitive,
                "soft": soft,
            },
        )

    def output_matches_regex(self, pattern: str, *, soft: bool = False) -> ExpectChain:
        """Assert output message matches a regex pattern."""
        return self._add(
            TYPE_CONTENT,
            {
                "target": "output.message",
                "check": "regex_match",
                "value": pattern,
                "soft": soft,
            },
        )

    def output_has_all_keywords(
        self, keywords: list[str], *, case_sensitive: bool = False, soft: bool = False
    ) -> ExpectChain:
        """Assert output message contains all keywords."""
        return self._add(
            TYPE_CONTENT,
            {
                "target": "output.message",
                "check": "keyword_all",
                "values": keywords,
                "case_sensitive": case_sensitive,
                "soft": soft,
            },
        )

    def output_has_any_keyword(
        self, keywords: list[str], *, case_sensitive: bool = False, soft: bool = False
    ) -> ExpectChain:
        """Assert output message contains at least one keyword."""
        return self._add(
            TYPE_CONTENT,
            {
                "target": "output.message",
                "check": "keyword_any",
                "values": keywords,
                "case_sensitive": case_sensitive,
                "soft": soft,
            },
        )

    def output_forbids(self, terms: list[str]) -> ExpectChain:
        """Assert output message contains none of the forbidden terms."""
        return self._add(
            TYPE_CONTENT,
            {
                "target": "output.message",
                "check": "forbidden",
                "values": terms,
            },
        )

    def content_contains(
        self, target: str, value: str, *, case_sensitive: bool = False, soft: bool = False
    ) -> ExpectChain:
        """Generic content contains check on any target."""
        return self._add(
            TYPE_CONTENT,
            {
                "target": target,
                "check": "contains",
                "value": value,
                "case_sensitive": case_sensitive,
                "soft": soft,
            },
        )

    # ── Layer 5: Embedding Similarity ──

    def output_similar_to(
        self,
        reference: str,
        *,
        threshold: float = 0.8,
        model: str | None = None,
        soft: bool = False,
    ) -> ExpectChain:
        """Assert output is semantically similar to reference via embeddings."""
        return self._add(
            TYPE_EMBEDDING,
            {
                "target": "output.message",
                "reference": reference,
                "threshold": threshold,
                "model": model,
                "soft": soft,
            },
        )

    # ── Layer 6: LLM Judge ──

    def passes_judge(
        self,
        criteria: str,
        *,
        rubric: str = "default",
        threshold: float = 0.8,
        model: str | None = None,
        soft: bool = False,
    ) -> ExpectChain:
        """Assert output message passes LLM judge evaluation against given criteria."""
        return self._add(
            TYPE_LLM_JUDGE,
            {
                "target": "output.message",
                "criteria": criteria,
                "rubric": rubric,
                "threshold": threshold,
                "model": model,
                "soft": soft,
            },
        )


    # ── Layer 7: Trace Tree (Multi-Agent) ──

    def agent_called(self, agent_id: str, *, soft: bool = False) -> ExpectChain:
        """Assert a specific agent was called in the trace tree."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "agent_called", "agent_id": agent_id, "soft": soft},
        )

    def delegation_depth(self, max_depth: int, *, soft: bool = False) -> ExpectChain:
        """Assert trace tree depth does not exceed max_depth."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "delegation_depth", "max_depth": max_depth, "soft": soft},
        )

    def agent_output_contains(
        self, agent_id: str, value: str, *, case_sensitive: bool = False, soft: bool = False
    ) -> ExpectChain:
        """Assert a sub-agent's output contains a value."""
        return self._add(
            TYPE_TRACE_TREE,
            {
                "check": "agent_output_contains",
                "agent_id": agent_id,
                "value": value,
                "case_sensitive": case_sensitive,
                "soft": soft,
            },
        )

    def cross_agent_data_flow(
        self, from_agent: str, to_agent: str, field: str, *, soft: bool = False
    ) -> ExpectChain:
        """Assert data flows from one agent's output to another's input."""
        return self._add(
            TYPE_TRACE_TREE,
            {
                "check": "cross_agent_data_flow",
                "from_agent": from_agent,
                "to_agent": to_agent,
                "field": field,
                "soft": soft,
            },
        )

    def follows_transitions(
        self, transitions: list[tuple[str, str]], *, soft: bool = False
    ) -> ExpectChain:
        """Assert agent delegations follow the specified transitions."""
        return self._add(
            TYPE_TRACE_TREE,
            {
                "check": "follows_transitions",
                "transitions": [list(pair) for pair in transitions],
                "soft": soft,
            },
        )

    def aggregate_cost_under(self, max_cost: float, *, soft: bool = False) -> ExpectChain:
        """Assert aggregate cost across trace tree is under threshold."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "aggregate_cost", "operator": "lte", "value": max_cost, "soft": soft},
        )

    def aggregate_tokens_under(self, max_tokens: int, *, soft: bool = False) -> ExpectChain:
        """Assert aggregate tokens across trace tree is under threshold."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "aggregate_tokens", "operator": "lte", "value": max_tokens, "soft": soft},
        )

    def agent_ordered_before(
        self, agent_a: str, agent_b: str, *, soft: bool = False
    ) -> ExpectChain:
        """Assert agent_a started before agent_b in the trace tree."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "agent_ordered_before", "agent_a": agent_a, "agent_b": agent_b, "soft": soft},
        )

    def agents_overlap(self, agent_a: str, agent_b: str, *, soft: bool = False) -> ExpectChain:
        """Assert agent_a and agent_b ran concurrently (overlapping wall-clock time)."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "agents_overlap", "agent_a": agent_a, "agent_b": agent_b, "soft": soft},
        )

    def agent_wall_time_under(
        self, agent_id: str, max_ms: int, *, soft: bool = False
    ) -> ExpectChain:
        """Assert a specific agent's wall-clock duration is under max_ms."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "agent_wall_time_under", "agent_id": agent_id, "max_ms": max_ms, "soft": soft},
        )

    def ordered_agents(
        self, groups: list[list[str]], *, soft: bool = False
    ) -> ExpectChain:
        """Assert agents ran in the specified ordered groups (parallel within, sequential across)."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "ordered_agents", "groups": groups, "soft": soft},
        )


def expect(result: AgentResult) -> ExpectChain:
    """Create an assertion chain for an agent result.

    Usage:
        expect(result).output_contains("refund").cost_under(0.01)
    """
    return ExpectChain(result)
