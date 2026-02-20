"""Multi-agent research pipeline — demonstrates Layer 8 (Trace Tree) assertions."""

from __future__ import annotations

from typing import Any

from attest.agent import agent
from attest.delegate import delegate
from attest.trace import TraceBuilder


@agent("orchestrator")
def research_pipeline(builder: TraceBuilder, topic: str) -> dict[str, Any]:
    """Orchestrate a research pipeline with researcher and writer sub-agents.

    Delegation chain: orchestrator -> researcher -> (tools)
                      orchestrator -> writer -> (tools)

    Uses attest.delegate() to create child traces linked to the parent.
    """
    builder.add_llm_call(
        name="gpt-4.1",
        args={"model": "gpt-4.1", "messages": [{"role": "user", "content": f"Research: {topic}"}]},
        result={"completion": "I'll coordinate the research and writing."},
    )

    # Delegate to researcher sub-agent
    with delegate("researcher") as researcher:
        researcher.set_input(query=topic)
        researcher.add_tool_call(
            name="web_search",
            args={"q": f"{topic} latest developments"},
            result={"results": ["Result 1: AI testing improves reliability", "Result 2: New frameworks"]},
        )
        researcher.add_tool_call(
            name="arxiv_search",
            args={"q": topic},
            result={"papers": [{"title": "AI Agent Testing", "year": 2026}]},
        )
        researcher.add_llm_call(
            name="gpt-4.1",
            args={"model": "gpt-4.1", "messages": [{"role": "user", "content": "Synthesize findings"}]},
            result={"completion": "AI testing frameworks are evolving rapidly. Key trends: ..."},
        )
        researcher.set_output(
            message="AI testing frameworks are evolving rapidly. Key trends include automated evaluation.",
        )
        researcher.set_metadata(total_tokens=300, cost_usd=0.008, latency_ms=3000, model="gpt-4.1")

    # Delegate to writer sub-agent
    with delegate("writer") as writer:
        writer.set_input(
            research="AI testing frameworks are evolving rapidly. Key trends include automated evaluation.",
        )
        writer.add_llm_call(
            name="gpt-4.1",
            args={"model": "gpt-4.1", "messages": [{"role": "user", "content": "Write summary"}]},
            result={
                "completion": (
                    "AI agent testing has matured significantly. Modern frameworks like Attest "
                    "provide 8-layer assertion pipelines covering schema validation through "
                    "multi-agent simulation."
                ),
            },
        )
        writer.add_tool_call(
            name="format_markdown",
            args={"style": "report"},
            result={"formatted": True},
        )
        writer.set_output(
            message=(
                "AI agent testing has matured significantly. Modern frameworks like Attest "
                "provide 8-layer assertion pipelines."
            ),
        )
        writer.set_metadata(total_tokens=250, cost_usd=0.006, latency_ms=2500, model="gpt-4.1")

    builder.set_metadata(total_tokens=100, cost_usd=0.003, latency_ms=1000, model="gpt-4.1")

    return {
        "message": (
            "Research complete. AI agent testing has matured significantly. "
            "Modern frameworks provide multi-layer assertion pipelines."
        ),
    }
