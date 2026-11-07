"""Shared test fixtures for Attest SDK tests."""

from __future__ import annotations

import stat
from collections.abc import Callable, Generator
from pathlib import Path

import pytest

from attest import trials
from attest._proto.types import (
    STATUS_PASS,
    TYPE_CONSTRAINT,
    TYPE_CONTENT,
    TYPE_SCHEMA,
    TYPE_TRACE,
    Assertion,
    AssertionResult,
    Trace,
)
from attest.result import AgentResult
from attest.trace import TraceBuilder

CHAIN_TEARDOWN_MARKER = "chain_teardown"


def pytest_configure(config: pytest.Config) -> None:
    config.addinivalue_line(
        "markers",
        f"{CHAIN_TEARDOWN_MARKER}: keep the plugin's check that fails a test with an expect() "
        "chain it never evaluated (the DSL unit tests of this suite build chains only to read "
        "their specs, so the check is off for them)",
    )


@pytest.hookimpl(hookwrapper=True, trylast=True)
def pytest_runtest_call(item: pytest.Item) -> Generator[None, None, None]:
    """Mark the chains a unit test built as evaluated before the attest plugin's teardown
    check looks at them, unless the test is marked ``chain_teardown``."""
    yield
    if item.get_closest_marker(CHAIN_TEARDOWN_MARKER) is None and trials._ledgers:
        for chain in trials._ledgers[-1]:
            chain._evaluated_assertion_count = len(chain.assertions)


@pytest.fixture
def sample_trace() -> Trace:
    """A sample refund agent trace for testing."""
    return (
        TraceBuilder(agent_id="refund-agent")
        .set_trace_id("trc_test_refund")
        .set_input(user_message="I want a refund for order ORD-123")
        .add_llm_call(
            "reasoning",
            args={"model": "gpt-4.1"},
            result={"completion": "I need to look up the order."},
            metadata={"duration_ms": 1200},
        )
        .add_tool_call(
            "lookup_order",
            args={"order_id": "ORD-123"},
            result={"status": "delivered", "amount": 89.99, "eligible_for_refund": True},
            metadata={"duration_ms": 45},
        )
        .add_tool_call(
            "process_refund",
            args={"order_id": "ORD-123", "amount": 89.99},
            result={"refund_id": "RFD-001", "estimated_days": 3},
            metadata={"duration_ms": 120},
        )
        .set_output(
            message=(
                "Your refund of $89.99 has been processed. "
                "You'll see it in 3 business days. Refund ID: RFD-001."
            ),
            structured={"refund_id": "RFD-001", "confidence": 0.95},
        )
        .set_metadata(
            total_tokens=1350,
            cost_usd=0.0067,
            latency_ms=4200,
            model="gpt-4.1",
        )
        .build()
    )


@pytest.fixture
def sample_result(sample_trace: Trace) -> AgentResult:
    """A sample AgentResult with passing assertions."""
    return AgentResult(
        trace=sample_trace,
        assertion_results=[
            AssertionResult(
                assertion_id="a1",
                status=STATUS_PASS,
                score=1.0,
                explanation="Schema validation passed",
            ),
            AssertionResult(
                assertion_id="a2",
                status=STATUS_PASS,
                score=1.0,
                explanation="Cost under budget",
            ),
        ],
    )


@pytest.fixture
def embedding_result() -> AgentResult:
    """An AgentResult with a known output message for embedding similarity testing."""
    trace = (
        TraceBuilder(agent_id="embed-test-agent")
        .set_trace_id("trc_test_embed")
        .set_input(user_message="What is the capital of France?")
        .add_llm_call(
            "reasoning",
            args={"model": "gpt-4.1"},
            result={"completion": "Paris is the capital of France."},
            metadata={"duration_ms": 800},
        )
        .set_output(
            message="Paris is the capital of France.",
            structured={"answer": "Paris", "confidence": 0.99},
        )
        .set_metadata(
            total_tokens=120,
            cost_usd=0.0002,
            latency_ms=900,
            model="gpt-4.1",
        )
        .build()
    )
    return AgentResult(
        trace=trace,
        assertion_results=[
            AssertionResult(
                assertion_id="embed_1",
                status=STATUS_PASS,
                score=0.95,
                explanation="Embedding similarity above threshold",
            ),
        ],
    )


@pytest.fixture
def judge_result() -> AgentResult:
    """An AgentResult with a known output message for LLM judge testing."""
    trace = (
        TraceBuilder(agent_id="judge-test-agent")
        .set_trace_id("trc_test_judge")
        .set_input(user_message="Explain how a transformer model works.")
        .add_llm_call(
            "reasoning",
            args={"model": "gpt-4.1"},
            result={
                "completion": (
                    "A transformer uses attention mechanisms to process sequences in parallel."
                )
            },
            metadata={"duration_ms": 1500},
        )
        .set_output(
            message=(
                "A transformer uses attention mechanisms to process sequences "
                "in parallel, enabling efficient training on large datasets."
            ),
            structured={"topic": "transformers", "confidence": 0.92},
        )
        .set_metadata(
            total_tokens=350,
            cost_usd=0.0008,
            latency_ms=1600,
            model="gpt-4.1",
        )
        .build()
    )
    return AgentResult(
        trace=trace,
        assertion_results=[
            AssertionResult(
                assertion_id="judge_1",
                status=STATUS_PASS,
                score=0.88,
                explanation="Response meets clarity and accuracy criteria",
            ),
        ],
    )


@pytest.fixture
def sample_assertions() -> list[Assertion]:
    """A sample set of L1-L4 assertions."""
    return [
        Assertion(
            assertion_id="schema_1",
            type=TYPE_SCHEMA,
            spec={
                "target": "steps[?name=='lookup_order'].result",
                "schema": {
                    "type": "object",
                    "required": ["status", "amount"],
                    "properties": {
                        "status": {"type": "string"},
                        "amount": {"type": "number"},
                    },
                },
            },
        ),
        Assertion(
            assertion_id="constraint_1",
            type=TYPE_CONSTRAINT,
            spec={
                "field": "metadata.cost_usd",
                "operator": "lte",
                "value": 0.01,
            },
        ),
        Assertion(
            assertion_id="trace_1",
            type=TYPE_TRACE,
            spec={
                "check": "contains_in_order",
                "tools": ["lookup_order", "process_refund"],
            },
        ),
        Assertion(
            assertion_id="content_1",
            type=TYPE_CONTENT,
            spec={
                "target": "output.message",
                "check": "contains",
                "value": "refund",
            },
        ),
    ]


ScriptedEngine = Callable[..., Path]

_SCRIPTED_ENGINE = """#!/bin/sh
case "$1" in
  version) {version_cmd} ;;
  help) {help_cmd} ;;
  *) echo "forwarded: $*"; {after_forward} ;;
esac
"""

_HELP_MODES = {
    # The command registry's help output.
    "registry": "echo 'usage: attest-engine <command> [args]'; echo 'commands:'{command_lines}",
    # An engine that predates the registry: `help` starts the server, which exits on EOF.
    "server": "true",
    # An engine that never answers.
    "hang": "sleep 30",
}


@pytest.fixture
def scripted_engine(tmp_path: Path) -> ScriptedEngine:
    """Return a factory writing an executable shell script that behaves like attest-engine.

    ``version`` prints ``attest-engine <version>`` (``version`` defaults to ENGINE_VERSION; an
    empty string prints nothing, like a binary without the subcommand). ``help`` follows
    ``help_mode`` (see ``_HELP_MODES``) and lists ``commands``. Any other command echoes its
    arguments and exits ``exit_code``, or kills itself with ``signal`` when given.
    """

    def make(
        path: Path | None = None,
        version: str | None = None,
        exit_code: int = 0,
        *,
        help_mode: str = "registry",
        commands: tuple[str, ...] = ("doctor", "policy"),
        signal: str | None = None,
    ) -> Path:
        from attest import ENGINE_VERSION

        target = path or tmp_path / "engine" / "attest-engine"
        target.parent.mkdir(parents=True, exist_ok=True)
        reported = ENGINE_VERSION if version is None else version
        command_lines = "".join(f"; echo '  {name}  {name} command'" for name in commands)
        target.write_text(
            _SCRIPTED_ENGINE.format(
                version_cmd="true" if reported == "" else f"echo 'attest-engine {reported}'",
                help_cmd=_HELP_MODES[help_mode].format(command_lines=command_lines),
                after_forward=f"kill -{signal} $$" if signal else f"exit {exit_code}",
            )
        )
        target.chmod(target.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
        return target

    return make


@pytest.fixture
def isolated_engine_env(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Isolate engine discovery: HOME and PATH point into tmp_path, download is disabled.

    Returns the redirected HOME directory.
    """
    home = tmp_path / "home"
    path_dir = tmp_path / "path-bin"
    home.mkdir()
    path_dir.mkdir()
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("PATH", str(path_dir))
    monkeypatch.setenv("ATTEST_ENGINE_NO_DOWNLOAD", "1")
    monkeypatch.delenv("ATTEST_ENGINE_PATH", raising=False)
    return home
