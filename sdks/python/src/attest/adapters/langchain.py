"""LangChain callback adapter for Attest."""

from __future__ import annotations

import time
from collections.abc import Generator
from contextlib import contextmanager
from typing import TYPE_CHECKING, Any
from uuid import UUID

from attest._proto.types import Trace
from attest.adapters._base import BaseAdapter

if TYPE_CHECKING:
    from langchain_core.messages import BaseMessage
    from langchain_core.outputs import LLMResult


def _require_langchain() -> None:
    """Raise ImportError if langchain-core is not installed."""
    try:
        import langchain_core  # noqa: F401
    except ImportError:
        raise ImportError("Install langchain extras: uv add 'attest-ai[langchain]'")


class LangChainCallbackHandler(BaseAdapter):
    """Accumulates LangChain callback events and builds an Attest Trace.

    Usage::

        handler = LangChainCallbackHandler(agent_id="my-agent")
        result = agent.invoke(input, config={"callbacks": [handler]})
        trace = handler.build_trace()
    """

    # Attributes expected by langchain_core.callbacks.manager.handle_event
    ignore_chain: bool = False
    ignore_llm: bool = False
    ignore_agent: bool = False
    ignore_retriever: bool = False
    ignore_chat_model: bool = False
    ignore_retry: bool = True
    raise_error: bool = False
    run_inline: bool = False

    def __init__(self, agent_id: str | None = None) -> None:
        super().__init__(agent_id=agent_id)
        _require_langchain()
        self._input: str | None = None
        self._output: str | None = None
        self._steps: list[dict[str, Any]] = []
        self._tool_starts: dict[str, dict[str, Any]] = {}
        self._llm_starts: dict[str, dict[str, Any]] = {}
        self._total_tokens: int = 0
        self._model: str | None = None
        self._start_time: float | None = None
        self._built = False

    # ------------------------------------------------------------------
    # Chain callbacks
    # ------------------------------------------------------------------

    def on_chain_start(
        self,
        serialized: dict[str, Any],
        inputs: dict[str, Any],
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        if parent_run_id is None:
            self._start_time = time.monotonic()
            if isinstance(inputs, dict):
                msg = inputs.get("input") or inputs.get("messages") or inputs.get("query", "")
                if isinstance(msg, str):
                    self._input = msg
                elif isinstance(msg, list) and msg:
                    first = msg[0]
                    self._input = first.content if hasattr(first, "content") else str(first)
                else:
                    self._input = str(msg) if msg else ""

    def on_chain_end(
        self,
        outputs: dict[str, Any],
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        if parent_run_id is None:
            if isinstance(outputs, dict):
                # LangGraph returns {"messages": [BaseMessage, ...]} — extract last AI message
                messages = outputs.get("messages")
                if isinstance(messages, list) and messages:
                    last = messages[-1]
                    if hasattr(last, "content"):
                        self._output = str(last.content)
                        return

                out = outputs.get("output") or outputs.get("result") or outputs.get("text", "")
                if isinstance(out, str):
                    self._output = out
                elif hasattr(out, "content"):
                    self._output = str(out.content)
                else:
                    self._output = str(out) if out else ""

    # ------------------------------------------------------------------
    # LLM callbacks
    # ------------------------------------------------------------------

    def on_chat_model_start(
        self,
        serialized: dict[str, Any],
        messages: list[list[BaseMessage]],
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        invocation_params: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        model_name: str | None = None
        if invocation_params:
            model_name = invocation_params.get("model_name") or invocation_params.get("model")
        self._llm_starts[str(run_id)] = {
            "model_name": model_name,
            "messages": messages,
            "start_time": time.monotonic(),
            "started_at_ms": self._now_ms(),
        }

    def on_llm_end(
        self,
        response: LLMResult,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        run_key = str(run_id)
        start_info = self._llm_starts.pop(run_key, {})
        model_name = start_info.get("model_name")
        start_time = start_info.get("start_time")
        started_at_ms: int | None = start_info.get("started_at_ms")
        ended_at_ms = self._now_ms()

        # Extract token usage
        input_tokens = 0
        output_tokens = 0
        token_usage: dict[str, Any] = {}
        if response.llm_output:
            token_usage = response.llm_output.get("token_usage", {})
            input_tokens = token_usage.get("prompt_tokens", 0) or 0
            output_tokens = token_usage.get("completion_tokens", 0) or 0
            if model_name is None:
                model_name = response.llm_output.get("model_name")

        total = input_tokens + output_tokens
        self._total_tokens += total

        if model_name and self._model is None:
            self._model = model_name

        # Extract completion text
        completion = ""
        if response.generations and response.generations[0]:
            completion = response.generations[0][0].text

        args: dict[str, Any] = {}
        if model_name:
            args["model"] = model_name

        result: dict[str, Any] = {}
        if completion:
            result["completion"] = completion
        if input_tokens:
            result["input_tokens"] = input_tokens
        if output_tokens:
            result["output_tokens"] = output_tokens

        metadata: dict[str, Any] = {}
        if start_time is not None:
            metadata["duration_ms"] = int((time.monotonic() - start_time) * 1000)

        self._steps.append({
            "type": "llm_call",
            "name": model_name or "llm",
            "args": args,
            "result": result,
            "metadata": metadata,
            "started_at_ms": started_at_ms,
            "ended_at_ms": ended_at_ms,
        })

    # ------------------------------------------------------------------
    # Tool callbacks
    # ------------------------------------------------------------------

    def on_tool_start(
        self,
        serialized: dict[str, Any],
        input_str: str,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        tool_name = serialized.get("name", "unknown_tool")
        self._tool_starts[str(run_id)] = {
            "name": tool_name,
            "input": input_str,
            "start_time": time.monotonic(),
            "started_at_ms": self._now_ms(),
        }

    def on_tool_end(
        self,
        output: str,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        run_key = str(run_id)
        start_info = self._tool_starts.pop(run_key, {})
        tool_name = start_info.get("name", "unknown_tool")
        start_time = start_info.get("start_time")
        tool_input = start_info.get("input", "")
        started_at_ms: int | None = start_info.get("started_at_ms")
        ended_at_ms = self._now_ms()

        metadata: dict[str, Any] = {}
        if start_time is not None:
            metadata["duration_ms"] = int((time.monotonic() - start_time) * 1000)

        # LangGraph passes ToolMessage objects; extract .content or coerce to str
        if hasattr(output, "content"):
            output_str = str(output.content)
        else:
            output_str = str(output)

        self._steps.append({
            "type": "tool_call",
            "name": tool_name,
            "args": {"input": tool_input},
            "result": {"output": output_str},
            "metadata": metadata,
            "started_at_ms": started_at_ms,
            "ended_at_ms": ended_at_ms,
        })

    def on_tool_error(
        self,
        error: BaseException,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        run_key = str(run_id)
        start_info = self._tool_starts.pop(run_key, {})
        tool_name = start_info.get("name", "unknown_tool")
        start_time = start_info.get("start_time")
        tool_input = start_info.get("input", "")
        started_at_ms: int | None = start_info.get("started_at_ms")
        ended_at_ms = self._now_ms()

        metadata: dict[str, Any] = {}
        if start_time is not None:
            metadata["duration_ms"] = int((time.monotonic() - start_time) * 1000)

        self._steps.append({
            "type": "tool_call",
            "name": tool_name,
            "args": {"input": tool_input},
            "result": {"error": str(error)},
            "metadata": metadata,
            "started_at_ms": started_at_ms,
            "ended_at_ms": ended_at_ms,
        })

    # ------------------------------------------------------------------
    # Build trace
    # ------------------------------------------------------------------

    def build_trace(self) -> Trace:
        """Finalize and return the accumulated Trace.

        Raises RuntimeError if called more than once.
        """
        if self._built:
            raise RuntimeError("build_trace() already called on this handler")
        self._built = True

        builder = self._create_builder()

        if self._input:
            builder.set_input_dict({"message": self._input})

        for step in self._steps:
            if step["type"] == "llm_call":
                builder.add_llm_call(
                    name=step["name"],
                    args=step.get("args"),
                    result=step.get("result"),
                    metadata=step.get("metadata"),
                    started_at_ms=step.get("started_at_ms"),
                    ended_at_ms=step.get("ended_at_ms"),
                    agent_id=self._agent_id,
                )
            elif step["type"] == "tool_call":
                builder.add_tool_call(
                    name=step["name"],
                    args=step.get("args"),
                    result=step.get("result"),
                    metadata=step.get("metadata"),
                    started_at_ms=step.get("started_at_ms"),
                    ended_at_ms=step.get("ended_at_ms"),
                    agent_id=self._agent_id,
                )

        latency_ms: int | None = None
        if self._start_time is not None:
            latency_ms = int((time.monotonic() - self._start_time) * 1000)

        builder.set_output(message=self._output or "")
        builder.set_metadata(
            total_tokens=self._total_tokens if self._total_tokens > 0 else None,
            latency_ms=latency_ms,
            model=self._model,
        )

        return builder.build()


class LangChainAdapter(BaseAdapter):
    """Context-manager wrapper around LangChainCallbackHandler.

    Usage::

        adapter = LangChainAdapter(agent_id="my-agent")
        with adapter.capture() as handler:
            agent.invoke(input, config={"callbacks": [handler]})
        trace = adapter.trace
    """

    def __init__(self, agent_id: str | None = None) -> None:
        super().__init__(agent_id=agent_id)
        self._trace: Trace | None = None

    @property
    def trace(self) -> Trace | None:
        return self._trace

    @contextmanager
    def capture(self) -> Generator[LangChainCallbackHandler, None, None]:
        """Yield a callback handler; auto-build trace on exit."""
        handler = LangChainCallbackHandler(agent_id=self._agent_id)
        yield handler
        self._trace = handler.build_trace()
