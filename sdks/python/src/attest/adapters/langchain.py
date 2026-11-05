"""LangChain callback adapter for Attest."""

from __future__ import annotations

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


def _duration(started_at_ms: int | None, ended_at_ms: int) -> dict[str, Any]:
    """Step metadata: the span between the step's own start and end timestamps."""
    return {} if started_at_ms is None else {"duration_ms": ended_at_ms - started_at_ms}


def _usage_metadata(response: LLMResult) -> dict[str, Any] | None:
    """``usage_metadata`` of the first generation's message, when the model reported it."""
    for generations in response.generations:
        for generation in generations:
            usage = getattr(getattr(generation, "message", None), "usage_metadata", None)
            if usage:
                return dict(usage)
    return None


def _message_text(content: Any) -> str:
    """Text of a message ``content``: a string, or a list of strings and content blocks."""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(
            block if isinstance(block, str) else str(block.get("text", ""))
            for block in content
            if isinstance(block, str) or (isinstance(block, dict) and block.get("type") == "text")
        )
    return str(content)


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
        self._started_at_ms: int | None = None
        self._ended_at_ms: int | None = None
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
            self._started_at_ms = self._now_ms()
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
            self._ended_at_ms = self._now_ms()
            if isinstance(outputs, dict):
                # LangGraph returns {"messages": [BaseMessage, ...]} — extract last AI message
                messages = outputs.get("messages")
                if isinstance(messages, list) and messages:
                    last = messages[-1]
                    if hasattr(last, "content"):
                        self._output = _message_text(last.content)
                        return

                out = outputs.get("output") or outputs.get("result") or outputs.get("text", "")
                if isinstance(out, str):
                    self._output = out
                elif hasattr(out, "content"):
                    self._output = _message_text(out.content)
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
        started_at_ms: int | None = start_info.get("started_at_ms")
        ended_at_ms = self._now_ms()

        # Token usage: the message's usage_metadata (current langchain-core), else the
        # provider's llm_output["token_usage"] (older integrations).
        input_tokens = 0
        output_tokens = 0
        total = 0
        usage = _usage_metadata(response)
        if usage:
            input_tokens = usage.get("input_tokens", 0) or 0
            output_tokens = usage.get("output_tokens", 0) or 0
            total = usage.get("total_tokens", 0) or input_tokens + output_tokens
        if response.llm_output:
            if not usage:
                token_usage = response.llm_output.get("token_usage", {})
                input_tokens = token_usage.get("prompt_tokens", 0) or 0
                output_tokens = token_usage.get("completion_tokens", 0) or 0
                total = input_tokens + output_tokens
            if model_name is None:
                model_name = response.llm_output.get("model_name")

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
        if total:
            result["tokens"] = total
        if input_tokens:
            result["input_tokens"] = input_tokens
        if output_tokens:
            result["output_tokens"] = output_tokens

        metadata = _duration(started_at_ms, ended_at_ms)

        self._steps.append(
            {
                "type": "llm_call",
                "name": model_name or "llm",
                "args": args,
                "result": result,
                "metadata": metadata,
                "started_at_ms": started_at_ms,
                "ended_at_ms": ended_at_ms,
            }
        )

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
        inputs: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        tool_name = serialized.get("name", "unknown_tool")
        # ``inputs`` is the tool's structured input; ``input_str`` is its Python repr.
        self._tool_starts[str(run_id)] = {
            "name": tool_name,
            "input": inputs if isinstance(inputs, dict) else {"input": input_str},
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
        tool_input = start_info.get("input", {})
        started_at_ms: int | None = start_info.get("started_at_ms")
        ended_at_ms = self._now_ms()

        metadata = _duration(started_at_ms, ended_at_ms)

        # LangGraph passes ToolMessage objects; their content is text or content blocks.
        output_str = _message_text(output.content) if hasattr(output, "content") else str(output)

        self._steps.append(
            {
                "type": "tool_call",
                "name": tool_name,
                "args": tool_input,
                "result": {"output": output_str},
                "metadata": metadata,
                "started_at_ms": started_at_ms,
                "ended_at_ms": ended_at_ms,
            }
        )

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
        tool_input = start_info.get("input", {})
        started_at_ms: int | None = start_info.get("started_at_ms")
        ended_at_ms = self._now_ms()

        metadata = _duration(started_at_ms, ended_at_ms)

        self._steps.append(
            {
                "type": "tool_call",
                "name": tool_name,
                "args": tool_input,
                "result": {"error": str(error)},
                "metadata": metadata,
                "started_at_ms": started_at_ms,
                "ended_at_ms": ended_at_ms,
            }
        )

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
        if self._started_at_ms is not None and self._ended_at_ms is not None:
            latency_ms = self._ended_at_ms - self._started_at_ms

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
