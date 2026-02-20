"""Google ADK (Agent Development Kit) adapter for Attest."""

from __future__ import annotations

from collections.abc import Sequence
from typing import Any

from attest._proto.types import STEP_AGENT_CALL, Step, Trace
from attest.trace import TraceBuilder


def _require_adk() -> None:
    """Raise ImportError if google-adk is not installed."""
    try:
        import google.adk  # noqa: F401
    except ImportError:
        raise ImportError("Install ADK extras: uv add 'attest-ai[google-adk]'")


class GoogleADKAdapter:
    """Maps Google ADK events to Attest traces.

    Event mapping:
    - event.actions.tool_calls -> tool_call steps
    - event.actions.tool_results -> tool_call steps (result-only)
    - event.actions.transfer_to_agent -> agent_call steps
    - event.usage_metadata.total_token_count -> token accumulation
    - event.is_final_response() + event.content.parts[].text -> output text
    - event.llm_response.model_version -> model name (first non-None wins)
    """

    def __init__(self, agent_id: str | None = None) -> None:
        self._agent_id = agent_id

    async def capture_async(
        self,
        runner: Any,
        user_id: str,
        session_id: str,
        message: str,
        **metadata: Any,
    ) -> Trace:
        """Run an ADK agent via runner.run_async() and capture the trace.

        Args:
            runner: An ADK Runner instance.
            user_id: User identifier for the ADK session.
            session_id: Session identifier for the ADK session.
            message: The user message to send to the agent.
            **metadata: Additional metadata to attach.

        Returns:
            Attest Trace populated from the ADK events.
        """
        _require_adk()

        from google.genai.types import Content, Part

        user_content = Content(
            role="user",
            parts=[Part(text=message)],
        )

        events: list[Any] = []
        async for event in runner.run_async(
            user_id=user_id,
            session_id=session_id,
            new_message=user_content,
        ):
            events.append(event)

        return self._build_trace(events, message)

    @classmethod
    def from_events(
        cls,
        events: Sequence[Any],
        agent_id: str | None = None,
        input_message: str = "",
        **metadata: Any,
    ) -> Trace:
        """Build an Attest Trace from pre-collected ADK events.

        Args:
            events: Sequence of ADK Event objects.
            agent_id: Optional agent identifier for the trace.
            input_message: The original user message.
            **metadata: Additional metadata.

        Returns:
            Attest Trace populated from the events.
        """
        _require_adk()

        adapter = cls(agent_id=agent_id)
        return adapter._build_trace(list(events), input_message)

    def _build_trace(self, events: list[Any], input_message: str = "") -> Trace:
        """Internal trace builder from ADK events."""
        builder = TraceBuilder(agent_id=self._agent_id)

        if input_message:
            builder.set_input_dict({"message": input_message})

        output_parts: list[str] = []
        total_tokens: int = 0
        model: str | None = None

        for event in events:
            # Extract tool calls
            actions = getattr(event, "actions", None)
            if actions is not None:
                tool_calls = getattr(actions, "tool_calls", None)
                if tool_calls:
                    for tc in tool_calls:
                        name = getattr(tc, "name", str(tc))
                        args = getattr(tc, "args", None)
                        call_args: dict[str, Any] | None = None
                        if args is not None:
                            call_args = dict(args) if hasattr(args, "items") else {"value": args}
                        builder.add_tool_call(name=name, args=call_args)

                tool_results = getattr(actions, "tool_results", None)
                if tool_results:
                    for tr in tool_results:
                        name = getattr(tr, "name", str(tr))
                        res = getattr(tr, "result", None)
                        call_result: dict[str, Any] | None = None
                        if res is not None:
                            call_result = dict(res) if hasattr(res, "items") else {"value": res}
                        builder.add_tool_call(name=name, args=None, result=call_result)

                transfer = getattr(actions, "transfer_to_agent", None)
                if transfer is not None:
                    builder.add_step(
                        Step(type=STEP_AGENT_CALL, name=str(transfer))
                    )

            # Extract token usage
            usage = getattr(event, "usage_metadata", None)
            if usage is not None:
                token_count = getattr(usage, "total_token_count", None)
                if token_count is not None:
                    total_tokens += int(token_count)

            # Extract final response text
            if event.is_final_response():
                content = getattr(event, "content", None)
                if content is not None:
                    parts = getattr(content, "parts", None)
                    if parts:
                        for part in parts:
                            text = getattr(part, "text", None)
                            if text:
                                output_parts.append(str(text))

            # Extract model version (first non-None wins)
            if model is None:
                llm_response = getattr(event, "llm_response", None)
                if llm_response is not None:
                    model_version = getattr(llm_response, "model_version", None)
                    if model_version is not None:
                        model = str(model_version)

        output_text = "".join(output_parts)

        # Record a single llm_call step for the overall LLM interaction
        builder.add_llm_call(
            "generate_content",
            args={"model": model} if model else None,
            result={"completion": output_text, "total_tokens": total_tokens},
        )

        builder.set_output_dict({"message": output_text})
        builder.set_metadata(
            total_tokens=total_tokens if total_tokens > 0 else None,
            model=model,
        )

        return builder.build()
