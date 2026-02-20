"""LlamaIndex instrumentation adapter for Attest."""

from __future__ import annotations

from typing import Any

from attest._proto.types import Trace
from attest.trace import TraceBuilder


def _require_llamaindex() -> None:
    """Raise ImportError if llama_index.core is not installed."""
    try:
        import llama_index.core  # noqa: F401
    except ImportError:
        raise ImportError("Install llamaindex extras: uv add 'attest-ai[llamaindex]'")


class LlamaIndexInstrumentationHandler:
    """Captures LlamaIndex events and converts them to Attest traces.

    Registers an event handler with the LlamaIndex global instrumentation
    dispatcher. Use as a context manager for automatic attach/detach lifecycle.

    Event mapping:
    - LLMChatStartEvent  -> buffers model name
    - LLMChatEndEvent    -> llm_call step with tokens and tool calls
    - RetrievalStartEvent -> buffers query string
    - RetrievalEndEvent  -> retrieval step with nodes and scores
    """

    def __init__(self, agent_id: str | None = None) -> None:
        self._agent_id = agent_id
        self._handler: Any = None
        self._llm_events: list[dict[str, Any]] = []
        self._retrieval_events: list[dict[str, Any]] = []
        self._current_model: str | None = None
        self._current_retrieval_query: str | None = None
        self._total_input_tokens: int = 0
        self._total_output_tokens: int = 0

    def attach(self) -> None:
        """Register the event handler with the LlamaIndex global dispatcher."""
        _require_llamaindex()

        from llama_index.core.instrumentation import get_dispatcher
        from llama_index.core.instrumentation.event_handlers import BaseEventHandler

        outer = self

        class _AttestEventHandlerImpl(BaseEventHandler):  # type: ignore[misc]
            """Internal event handler that accumulates LlamaIndex events."""

            @classmethod
            def class_name(cls) -> str:
                return "AttestEventHandler"

            def handle(self, event: Any, **kwargs: Any) -> None:
                outer._handle_event(event)

        self._handler = _AttestEventHandlerImpl()
        dispatcher = get_dispatcher()
        dispatcher.add_event_handler(self._handler)

    def detach(self) -> None:
        """Unregister the event handler from the LlamaIndex global dispatcher."""
        if self._handler is None:
            return

        from llama_index.core.instrumentation import get_dispatcher

        dispatcher = get_dispatcher()
        # LlamaIndex dispatcher stores handlers in a list attribute
        if hasattr(dispatcher, "event_handlers"):
            handlers = dispatcher.event_handlers
            if self._handler in handlers:
                handlers.remove(self._handler)

        self._handler = None

    def __enter__(self) -> LlamaIndexInstrumentationHandler:
        self.attach()
        return self

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        self.detach()

    def _handle_event(self, event: Any) -> None:
        """Route a LlamaIndex event to the appropriate accumulator."""
        event_type = type(event).__name__

        if event_type == "LLMChatStartEvent":
            self._handle_llm_start(event)
        elif event_type == "LLMChatEndEvent":
            self._handle_llm_end(event)
        elif event_type == "RetrievalStartEvent":
            self._handle_retrieval_start(event)
        elif event_type == "RetrievalEndEvent":
            self._handle_retrieval_end(event)

    def _handle_llm_start(self, event: Any) -> None:
        """Extract model name from LLMChatStartEvent."""
        model = None
        if hasattr(event, "model_dict") and isinstance(event.model_dict, dict):
            model = event.model_dict.get("model")
        if model is None and hasattr(event, "messages"):
            # Fallback: model may be on the event directly
            model = getattr(event, "model", None)
        if model is not None:
            self._current_model = str(model)

    def _handle_llm_end(self, event: Any) -> None:
        """Extract completion, tokens, and tool calls from LLMChatEndEvent."""
        completion = ""
        input_tokens = 0
        output_tokens = 0
        tool_calls: list[dict[str, Any]] = []

        response = getattr(event, "response", None)
        if response is not None:
            completion = str(response)

            # Token usage from raw response
            raw = getattr(response, "raw", None)
            if raw is not None and isinstance(raw, dict):
                usage = raw.get("usage", {})
                input_tokens = usage.get("prompt_tokens", 0)
                output_tokens = usage.get("completion_tokens", 0)

            # Tool calls from message additional_kwargs
            message = getattr(response, "message", None)
            if message is not None:
                additional = getattr(message, "additional_kwargs", {})
                if isinstance(additional, dict):
                    tool_calls = additional.get("tool_calls", [])

        self._total_input_tokens += input_tokens
        self._total_output_tokens += output_tokens

        llm_data: dict[str, Any] = {
            "model": self._current_model,
            "completion": completion,
            "input_tokens": input_tokens,
            "output_tokens": output_tokens,
            "tool_calls": tool_calls,
        }
        self._llm_events.append(llm_data)

    def _handle_retrieval_start(self, event: Any) -> None:
        """Buffer query from RetrievalStartEvent."""
        query = getattr(event, "str_or_query_bundle", None)
        if query is not None:
            self._current_retrieval_query = str(query)

    def _handle_retrieval_end(self, event: Any) -> None:
        """Extract nodes from RetrievalEndEvent."""
        nodes_data: list[dict[str, Any]] = []
        raw_nodes = getattr(event, "nodes", [])
        for node in raw_nodes:
            node_info: dict[str, Any] = {}
            if hasattr(node, "text"):
                node_info["text"] = str(node.text)
            if hasattr(node, "score"):
                node_info["score"] = node.score
            if hasattr(node, "node_id"):
                node_info["node_id"] = str(node.node_id)
            nodes_data.append(node_info)

        retrieval_data: dict[str, Any] = {
            "query": self._current_retrieval_query,
            "nodes": nodes_data,
        }
        self._retrieval_events.append(retrieval_data)
        self._current_retrieval_query = None

    def build_trace(
        self,
        query: str | None = None,
        response: str | None = None,
        latency_ms: int | None = None,
        cost_usd: float | None = None,
    ) -> Trace:
        """Build an Attest Trace from accumulated events.

        Args:
            query: The user query/input message.
            response: The final agent response text.
            latency_ms: Total latency in milliseconds.
            cost_usd: Total cost in USD.

        Returns:
            Attest Trace populated from captured LlamaIndex events.
        """
        builder = TraceBuilder(agent_id=self._agent_id)

        if query is not None:
            builder.set_input_dict({"message": query})

        # Add LLM call steps
        for llm_data in self._llm_events:
            args: dict[str, Any] = {}
            if llm_data["model"] is not None:
                args["model"] = llm_data["model"]

            result: dict[str, Any] = {
                "completion": llm_data["completion"],
            }
            if llm_data["input_tokens"]:
                result["input_tokens"] = llm_data["input_tokens"]
            if llm_data["output_tokens"]:
                result["output_tokens"] = llm_data["output_tokens"]

            builder.add_llm_call(
                name="chat_completion",
                args=args,
                result=result,
            )

            # Add tool call steps extracted from LLM response
            for tc in llm_data.get("tool_calls", []):
                func = tc.get("function", {})
                tc_name = func.get("name", "unknown_tool")
                tc_args = func.get("arguments", {})
                if isinstance(tc_args, str):
                    import json

                    try:
                        tc_args = json.loads(tc_args)
                    except (json.JSONDecodeError, ValueError):
                        tc_args = {"raw": tc_args}
                builder.add_tool_call(name=tc_name, args=tc_args)

        # Add retrieval steps
        for ret_data in self._retrieval_events:
            ret_args: dict[str, Any] = {}
            if ret_data["query"] is not None:
                ret_args["query"] = ret_data["query"]

            builder.add_retrieval(
                name="retrieve",
                args=ret_args,
                result={"nodes": ret_data["nodes"]},
            )

        # Set output
        if response is not None:
            builder.set_output(message=response)
        else:
            # Use last LLM completion as output fallback
            last_completion = ""
            if self._llm_events:
                last_completion = self._llm_events[-1].get("completion", "")
            builder.set_output(message=last_completion)

        # Set metadata
        total_tokens: int | None = None
        if self._total_input_tokens or self._total_output_tokens:
            total_tokens = self._total_input_tokens + self._total_output_tokens

        model: str | None = None
        if self._llm_events:
            model = self._llm_events[0].get("model")

        builder.set_metadata(
            total_tokens=total_tokens,
            cost_usd=cost_usd,
            latency_ms=latency_ms,
            model=model,
        )

        return builder.build()
