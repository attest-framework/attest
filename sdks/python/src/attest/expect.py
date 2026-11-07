"""Fluent assertion DSL for Attest agent tests."""

from __future__ import annotations

import json
import math
from dataclasses import dataclass
from typing import Any, Literal

from attest._proto.types import (
    TYPE_CONSTRAINT,
    TYPE_CONTENT,
    TYPE_EFFECT,
    TYPE_EMBEDDING,
    TYPE_LLM_JUDGE,
    TYPE_PLUGIN,
    TYPE_SCHEMA,
    TYPE_TRACE,
    TYPE_TRACE_TREE,
    Assertion,
    Trace,
)
from attest.result import AgentResult
from attest.trials import track_chain

TrajectoryMode = Literal["strict", "unordered", "subset", "superset"]
ArgOperator = Literal["eq", "ne", "lt", "lte", "gt", "gte", "in", "not_in"]


# Each effect assertion embeds the world snapshot, and the engine refuses a request line over
# 10 MiB; 1 MiB of that is left for the rest of the batch.
MAX_WORLD_BYTES = 9 * 1024 * 1024


def _merge_where(where: dict[str, Any] | None, filters: dict[str, Any]) -> dict[str, Any]:
    """Merge an explicit ``where`` dict with keyword filters; a key in both is an error."""
    merged = dict(where or {})
    clash = sorted(set(merged) & set(filters))
    if clash:
        raise TypeError(f"filter given both in where and as a keyword: {', '.join(clash)}")
    merged.update(filters)
    return merged


@dataclass(frozen=True)
class Related:
    """A record of another world resource, joined on a key: the only non-literal value the
    effect checks accept. Build it with :func:`related`."""

    resource: str
    field: str
    on: str

    def to_spec(self) -> dict[str, str]:
        """Return the engine's ``related`` object."""
        return {"resource": self.resource, "field": self.field, "on": self.on}


def related(resource: str, field: str, *, on: str) -> Related:
    """Name ``resource.field`` of the record whose ``on`` field equals this record's ``on``.

    Use it as the ``at_most`` bound of :meth:`WorldChecks.sum` or as the third argument of
    :meth:`WorldChecks.matches`. ``on`` is the key both resources carry (``order_id``).

    Raises:
        TypeError: An argument is not a non-empty string.
    """
    for name, value in (("resource", resource), ("field", field), ("on", on)):
        if not isinstance(value, str) or not value:
            raise TypeError(f"related: {name} must be a non-empty string, got {value!r}")
    return Related(resource, field, on)


def _require_name(check: str, name: str, value: object) -> None:
    if not isinstance(value, str) or not value:
        raise TypeError(f"{check}: {name} must be a non-empty string, got {value!r}")


class WorldChecks:
    """Effect checks against one world snapshot, from :meth:`ExpectChain.world`.

    Each method adds an ``effect`` assertion (Layer 3) to the parent chain and returns it.
    Keyword arguments, or the ``where`` dict (use it for fields named like a parameter, such as
    ``key`` or ``by``), are exact-match filters on the resource's records.
    """

    def __init__(self, chain: ExpectChain, snapshot: dict[str, Any] | None) -> None:
        self._chain = chain
        self._snapshot = snapshot
        # The snapshot is measured once, as the engine will read it (UTF-8, not \\u-escaped).
        self._size = len(
            json.dumps(snapshot, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        )

    def _add(self, spec: dict[str, Any], where: dict[str, Any]) -> ExpectChain:
        self._chain._world_bytes += self._size
        if self._chain._world_bytes > MAX_WORLD_BYTES:
            raise ValueError(
                f"effect checks would send the {self._size}-byte world snapshot "
                f"{self._chain._world_bytes // max(self._size, 1)} times "
                f"({self._chain._world_bytes} bytes), over the {MAX_WORLD_BYTES}-byte limit "
                "of one request; use fewer checks per test or scope the world to less state"
            )
        spec["world"] = self._snapshot
        if where:
            spec["where"] = where
        return self._chain._add(TYPE_EFFECT, spec)

    def count(
        self,
        resource: str,
        /,
        *,
        equals: int | None = None,
        at_most: int | None = None,
        at_least: int | None = None,
        where: dict[str, Any] | None = None,
        **filters: Any,
    ) -> ExpectChain:
        """Assert how many records of ``resource`` match ``where``.

        Give ``equals``, or ``at_most`` and/or ``at_least``. A violation reads
        ``effect violated: 2 refunds for O-1042 (expected 1)``.

        Raises:
            ValueError: No bound is given, or ``equals`` is combined with another bound.
        """
        if equals is None and at_most is None and at_least is None:
            raise ValueError("count needs equals, at_most or at_least")
        if equals is not None and (at_most is not None or at_least is not None):
            raise ValueError("count: equals excludes at_most and at_least")
        spec: dict[str, Any] = {"check": "count", "resource": resource}
        for key, bound in (("equals", equals), ("at_most", at_most), ("at_least", at_least)):
            if bound is not None:
                spec[key] = bound
        return self._add(spec, _merge_where(where, filters))

    def unique(
        self,
        resource: str,
        /,
        *,
        by: str | list[str],
        where: dict[str, Any] | None = None,
        **filters: Any,
    ) -> ExpectChain:
        """Assert no two matching records share the same values for field(s) ``by``."""
        fields = [by] if isinstance(by, str) else list(by)
        if not fields:
            raise ValueError("unique needs at least one field in by")
        return self._add(
            {"check": "unique", "resource": resource, "fields": fields},
            _merge_where(where, filters),
        )

    def idempotent(
        self,
        resource: str,
        /,
        *,
        key: str = "idempotency_key",
        where: dict[str, Any] | None = None,
        **filters: Any,
    ) -> ExpectChain:
        """Assert every matching record carries a distinct value in field ``key``, the
        record field that stores the idempotency key."""
        return self._add(
            {"check": "idempotent", "resource": resource, "field": key},
            _merge_where(where, filters),
        )

    def sum(
        self,
        resource: str,
        field: str,
        /,
        *,
        at_most: int | float | Related,
        where: dict[str, Any] | None = None,
        **filters: Any,
    ) -> ExpectChain:
        """Assert the sum of ``field`` over the matching records of ``resource`` stays within
        ``at_most``.

        With a number the filtered total must not exceed it. With :func:`related` the records
        are grouped by the ``on`` key and each group's sum must not exceed the related
        record's field. A violation reads ``effect violated: refunds amount_cents sum 13800
        for O-2077 exceeds orders.amount_cents 12600``.

        Raises:
            TypeError: ``resource`` or ``field`` is not a non-empty string, or ``at_most`` is
                neither a finite number nor a :func:`related`.
        """
        _require_name("sum", "resource", resource)
        _require_name("sum", "field", field)
        bound: float | dict[str, dict[str, str]]
        if isinstance(at_most, Related):
            bound = {"related": at_most.to_spec()}
        elif isinstance(at_most, bool) or not isinstance(at_most, (int, float)):
            raise TypeError(f"sum: at_most must be a number or related(...), got {at_most!r}")
        elif isinstance(at_most, float) and not math.isfinite(at_most):
            raise TypeError(f"sum: at_most must be finite, got {at_most!r}")
        else:
            bound = at_most
        return self._add(
            {"check": "sum", "resource": resource, "field": field, "at_most": bound},
            _merge_where(where, filters),
        )

    def matches(
        self,
        resource: str,
        field: str,
        target: Related,
        /,
        *,
        where: dict[str, Any] | None = None,
        **filters: Any,
    ) -> ExpectChain:
        """Assert ``field`` of every matching record of ``resource`` equals the related
        record's field, as named by ``target`` (:func:`related`).

        A violation reads ``effect violated: notifications customer_id C-0520 for O-2077 does
        not match orders.customer_id C-0311``.

        Raises:
            TypeError: ``resource`` or ``field`` is not a non-empty string, or ``target`` is
                not a :func:`related`.
        """
        _require_name("matches", "resource", resource)
        _require_name("matches", "field", field)
        if not isinstance(target, Related):
            raise TypeError(f"matches: the third argument must be related(...), got {target!r}")
        return self._add(
            {"check": "matches", "resource": resource, "field": field, "related": target.to_spec()},
            _merge_where(where, filters),
        )


class ExpectChain:
    """Fluent assertion builder. Collects assertions for batch evaluation."""

    def __init__(self, result: AgentResult) -> None:
        self._result = result
        self._assertions: list[Assertion] = []
        self._world_bytes = 0
        #: Number of assertions in the last evaluation snapshot; -1 means never evaluated.
        self._evaluated_assertion_count = -1
        track_chain(self)

    @property
    def evaluated(self) -> bool:
        """Whether the current assertions were sent for evaluation, including later additions."""
        return self._evaluated_assertion_count == len(self._assertions)

    @property
    def assertions(self) -> list[Assertion]:
        """Return collected assertions."""
        return list(self._assertions)

    @property
    def trace(self) -> Trace:
        """Return the trace from the result."""
        return self._result.trace

    def _add(self, assertion_type: str, spec: dict[str, Any]) -> ExpectChain:
        """Add an assertion to the collection.

        The assertion has no id yet: ``assertion_id`` is set when the chain is evaluated
        (``attest.assertion_id``), from the scope, type, spec and the occurrence among
        identical assertions in evaluation order.
        """
        self._assertions.append(
            Assertion(
                assertion_id="",
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

    # ── Layer 3: Behavior contracts (tool calls only, retries counted) ──

    def tool_cardinality(
        self,
        tool: str,
        *,
        per: str | None = None,
        at_most: int | None = None,
        at_least: int | None = None,
        soft: bool = False,
    ) -> ExpectChain:
        """Assert how many times a tool is called, per value of argument ``per`` when set.

        ``tool_cardinality("issue_refund", per="order_id", at_most=1)`` fails with
        ``issue_refund called 2× for order_id=O-1042 (max 1)``.
        """
        if at_most is None and at_least is None:
            raise ValueError("tool_cardinality needs at_most, at_least or both")
        spec: dict[str, Any] = {"check": "tool_cardinality", "tool": tool}
        if per is not None:
            spec["per"] = per
        if at_least is not None:
            spec["min"] = at_least
        if at_most is not None:
            spec["max"] = at_most
        spec["soft"] = soft
        return self._add(TYPE_TRACE, spec)

    def trajectory(
        self, tools: list[str], *, mode: TrajectoryMode = "strict", soft: bool = False
    ) -> ExpectChain:
        """Assert the tool-call trajectory matches ``tools``.

        Modes: ``strict`` (same sequence), ``unordered`` (same calls, any order),
        ``subset`` (no call outside ``tools``), ``superset`` (every call in ``tools``
        made). Use :meth:`trajectory_partial_order` for ordering constraints.
        """
        return self._add(
            TYPE_TRACE, {"check": "trajectory", "mode": mode, "tools": tools, "soft": soft}
        )

    def trajectory_partial_order(
        self,
        order: list[tuple[str, str]],
        *,
        tools: list[str] | None = None,
        soft: bool = False,
    ) -> ExpectChain:
        """Assert every tool in ``tools`` and ``order`` is called and each
        ``(before, after)`` pair holds as in :meth:`tool_called_before`."""
        spec: dict[str, Any] = {
            "check": "trajectory",
            "mode": "partial_order",
            "order": [[before, after] for before, after in order],
        }
        if tools is not None:
            spec["tools"] = tools
        spec["soft"] = soft
        return self._add(TYPE_TRACE, spec)

    def tool_called_before(self, before: str, after: str, *, soft: bool = False) -> ExpectChain:
        """Assert every call to ``after`` has an earlier call to ``before``.

        Holds when ``after`` is never called.
        """
        return self._add(
            TYPE_TRACE, {"check": "tool_order", "before": before, "after": after, "soft": soft}
        )

    def no_tool_cycles(
        self, period: int, *, min_repeats: int = 2, soft: bool = False
    ) -> ExpectChain:
        """Assert no block of ``period`` (1–4) tool calls repeats ``min_repeats``
        times in a row; A→B→A→B fails ``period=2``."""
        return self._add(
            TYPE_TRACE,
            {"check": "cycle", "period": period, "min_repeats": min_repeats, "soft": soft},
        )

    def max_tool_failure_streak(self, max_streak: int, *, soft: bool = False) -> ExpectChain:
        """Assert at most ``max_streak`` consecutive tool calls end in error (trace v2)."""
        return self._add(TYPE_TRACE, {"check": "failure_streak", "max": max_streak, "soft": soft})

    def tool_arg_satisfies(
        self, tool: str, arg: str, op: ArgOperator, value: Any, *, soft: bool = False
    ) -> ExpectChain:
        """Assert every call to ``tool`` has argument ``arg`` satisfying ``op value``.

        ``arg`` is a dot path into the call's arguments. Holds when ``tool`` is
        never called.
        """
        return self._add(
            TYPE_TRACE,
            {
                "check": "arg_predicate",
                "tool": tool,
                "arg": arg,
                "op": op,
                "value": value,
                "soft": soft,
            },
        )

    # ── Layer 3: Effect checks (the external system the agent acted on) ──

    def world(self, snapshot: dict[str, Any] | None) -> WorldChecks:
        """Check effects in a world snapshot: ``expect(trace).world(world.state()).count(...)``.

        ``snapshot`` is :meth:`WorldSession.state` (or the ``stop()`` result). ``None`` means
        the test ran without a world helper: the checks are ``not_evaluated``
        (``missing_evidence``).
        """
        return WorldChecks(self, snapshot)

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
        threshold: float | str = 0.8,
        model: str | None = None,
        soft: bool = False,
    ) -> ExpectChain:
        """Assert output is semantically similar to reference via embeddings.

        Args:
            reference: Text to compare output against.
            threshold: Minimum cosine similarity score (0.0–1.0) or ``"dynamic"``
                for σ-based drift detection against historical baselines.
            model: Override the embedding model.
            soft: When True the failure is non-blocking.
        """
        if isinstance(threshold, str) and threshold != "dynamic":
            msg = f'threshold must be a float or "dynamic", got {threshold!r}'
            raise ValueError(msg)
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
        threshold: float | str = 0.8,
        model: str | None = None,
        soft: bool = False,
    ) -> ExpectChain:
        """Assert output passes LLM judge evaluation against given criteria.

        Args:
            criteria: Natural language description of what makes a good response.
            rubric: Rubric name (default ``"default"``).
            threshold: Minimum passing score (0.0–1.0) or ``"dynamic"`` for
                σ-based drift detection against historical baselines.
            model: Override the judge model.
            soft: When True the failure is non-blocking.
        """
        if isinstance(threshold, str) and threshold != "dynamic":
            msg = f'threshold must be a float or "dynamic", got {threshold!r}'
            raise ValueError(msg)
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

    # ── Trace Tree (Multi-Agent, layer 3) ──

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
            {
                "check": "agent_wall_time_under",
                "agent_id": agent_id,
                "max_ms": max_ms,
                "soft": soft,
            },
        )

    def ordered_agents(self, groups: list[list[str]], *, soft: bool = False) -> ExpectChain:
        """Assert agents ran in ordered groups (parallel within, sequential across)."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "ordered_agents", "groups": groups, "soft": soft},
        )

    # ── TraceTree Analytics (layer 3) ──

    def aggregate_latency_under(self, max_ms: int, *, soft: bool = False) -> ExpectChain:
        """Assert aggregate latency across the trace tree is at or below max_ms."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "aggregate_latency", "operator": "lte", "value": max_ms, "soft": soft},
        )

    def all_tools_called(self, tool_names: list[str], *, soft: bool = False) -> ExpectChain:
        """Assert that all specified tools were called across the entire trace tree."""
        return self._add(
            TYPE_TRACE_TREE,
            {"check": "all_tools_called", "tools": tool_names, "soft": soft},
        )

    # ── Plugin ──

    def plugin(
        self,
        plugin_id: str,
        config: dict[str, Any] | None = None,
        *,
        soft: bool = False,
    ) -> ExpectChain:
        """Assert via a registered plugin (plugin assertion).

        Args:
            plugin_id: Identifier of the plugin registered with the engine.
            config: Optional plugin-specific configuration dict passed to the
                plugin at evaluation time.
            soft: When True the assertion is a soft failure — it will not
                block the test but will be recorded in the results.
        """
        spec: dict[str, Any] = {"plugin_id": plugin_id, "soft": soft}
        if config is not None:
            spec["config"] = config
        return self._add(TYPE_PLUGIN, spec)


def expect(result: AgentResult | Trace) -> ExpectChain:
    """Create an assertion chain for an agent result.

    Accepts either an ``AgentResult`` or a raw ``Trace``.  When a ``Trace``
    is passed it is automatically wrapped in an ``AgentResult``.

    Usage::

        expect(result).output_contains("refund").cost_under(0.01)
        expect(trace).required_tools(["search"])  # also works
    """
    if isinstance(result, Trace):
        result = AgentResult(trace=result)
    return ExpectChain(result)
