"""CrewAI adapter example — demonstrates trace capture from CrewAI crews.

The CrewAIAdapter constructor requires crewai to be installed. This example
shows two approaches:
1. Manual trace building (no crewai dependency) matching the adapter's output
2. Real CrewAI usage pattern (requires crewai + OPENAI_API_KEY)
"""

from __future__ import annotations

import types
from typing import Any

from attest._proto.types import STEP_AGENT_CALL, Step, Trace
from attest.trace import TraceBuilder


def build_mock_crew() -> types.SimpleNamespace:
    """Build a duck-typed crew matching CrewAI's attribute interface.

    The CrewAIAdapter uses getattr() for all field access, so SimpleNamespace
    objects with matching attributes work identically to real CrewAI types.
    """
    agent_researcher = types.SimpleNamespace(
        role="researcher",
        goal="Find relevant information",
        backstory="Expert at web research",
    )
    agent_writer = types.SimpleNamespace(
        role="writer",
        goal="Write clear summaries",
        backstory="Professional technical writer",
    )

    task_research = types.SimpleNamespace(
        description="Research AI testing frameworks",
        expected_output="A list of top frameworks with pros/cons",
    )
    task_write = types.SimpleNamespace(
        description="Write a summary report",
        expected_output="A 500-word summary",
    )

    return types.SimpleNamespace(
        description="Research and writing crew",
        agents=[agent_researcher, agent_writer],
        tasks=[task_research, task_write],
    )


def build_mock_crew_output() -> types.SimpleNamespace:
    """Build a duck-typed CrewOutput matching CrewAI's output interface."""
    task_output_1 = types.SimpleNamespace(
        description="Research AI testing frameworks",
        raw="Found 5 frameworks: Attest, Promptfoo, DeepEval, Ragas, Phoenix",
    )
    task_output_2 = types.SimpleNamespace(
        description="Write a summary report",
        raw="AI testing has evolved with frameworks providing multi-layer assertions.",
    )

    return types.SimpleNamespace(
        raw="AI testing report: 5 frameworks compared.",
        token_usage=types.SimpleNamespace(
            total_tokens=800,
            prompt_tokens=500,
            completion_tokens=300,
        ),
        tasks_output=[task_output_1, task_output_2],
    )


def build_crew_trace_manually() -> Trace:
    """Build a trace matching CrewAIAdapter.trace_from_crew_output() output.

    This replicates the adapter's trace structure without requiring crewai:
    - crew.agents -> agent_call steps
    - crew_output.tasks_output -> tool_call steps
    - crew_output.raw -> output message
    - crew_output.token_usage -> metadata
    """
    crew = build_mock_crew()
    crew_output = build_mock_crew_output()

    builder = TraceBuilder(agent_id="research-crew")

    # Input from crew description
    builder.set_input(description=crew.description)

    # Agent steps (matching adapter behavior)
    for agent_obj in crew.agents:
        builder.add_step(Step(
            type=STEP_AGENT_CALL,
            name=agent_obj.role,
            agent_id="research-crew",
        ))

    # Task output steps (matching adapter behavior)
    for task_out in crew_output.tasks_output:
        builder.add_tool_call(
            name=task_out.description,
            result={"output": task_out.raw},
            agent_id="research-crew",
        )

    # Output and metadata
    builder.set_output(message=crew_output.raw)
    builder.set_metadata(total_tokens=crew_output.token_usage.total_tokens)

    return builder.build()


def run_crew() -> dict[str, Any]:
    """Run the mock crew and return the trace."""
    trace = build_crew_trace_manually()
    return {
        "trace": trace,
        "output": trace.output["message"],
    }
