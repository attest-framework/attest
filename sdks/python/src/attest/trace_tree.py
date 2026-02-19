"""Multi-agent trace tree for cross-agent analysis."""

from __future__ import annotations

from dataclasses import dataclass

from attest._proto.types import STEP_AGENT_CALL, Trace


@dataclass
class TraceTree:
    """Multi-agent trace tree for cross-agent analysis."""

    root: Trace

    @property
    def agents(self) -> list[str]:
        """Return list of all agent_ids in the tree (including root)."""
        result: list[str] = []
        self._collect_agents(self.root, result)
        return result

    def _collect_agents(self, trace: Trace, acc: list[str]) -> None:
        if trace.agent_id is not None:
            acc.append(trace.agent_id)
        for step in trace.steps:
            if step.type == STEP_AGENT_CALL and step.sub_trace is not None:
                self._collect_agents(step.sub_trace, acc)

    def find_agent(self, agent_id: str) -> Trace | None:
        """Find a sub-trace by agent_id. Returns None if not found."""
        return self._find_agent(self.root, agent_id)

    def _find_agent(self, trace: Trace, agent_id: str) -> Trace | None:
        if trace.agent_id == agent_id:
            return trace
        for step in trace.steps:
            if step.type == STEP_AGENT_CALL and step.sub_trace is not None:
                found = self._find_agent(step.sub_trace, agent_id)
                if found is not None:
                    return found
        return None

    @property
    def depth(self) -> int:
        """Return max nesting depth of the trace tree. Root = 0."""
        return self._depth(self.root)

    def _depth(self, trace: Trace) -> int:
        max_child = -1
        for step in trace.steps:
            if step.type == STEP_AGENT_CALL and step.sub_trace is not None:
                child_depth = self._depth(step.sub_trace)
                if child_depth > max_child:
                    max_child = child_depth
        return max_child + 1 if max_child >= 0 else 0

    def flatten(self) -> list[Trace]:
        """Return all traces in the tree (depth-first)."""
        result: list[Trace] = []
        self._flatten(self.root, result)
        return result

    def _flatten(self, trace: Trace, acc: list[Trace]) -> None:
        acc.append(trace)
        for step in trace.steps:
            if step.type == STEP_AGENT_CALL and step.sub_trace is not None:
                self._flatten(step.sub_trace, acc)

    @property
    def aggregate_tokens(self) -> int:
        """Sum total_tokens across all traces in tree."""
        return sum(
            t.metadata.total_tokens or 0
            for t in self.flatten()
            if t.metadata is not None
        )

    @property
    def aggregate_cost(self) -> float:
        """Sum cost_usd across all traces in tree."""
        return sum(
            t.metadata.cost_usd or 0.0
            for t in self.flatten()
            if t.metadata is not None
        )

    @property
    def aggregate_latency(self) -> int:
        """Sum latency_ms across all traces in tree."""
        return sum(
            t.metadata.latency_ms or 0
            for t in self.flatten()
            if t.metadata is not None
        )
