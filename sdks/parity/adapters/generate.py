"""Generate the adapter conformance corpus: ``sdks/parity/adapters/<adapter>/<case>.json``.

Every case holds a provider response or framework run record built with the
official SDK types at the versions pinned in ``versions.json``, plus the golden
canonical trace both SDKs must produce from it. The goldens are written by hand
below; they are the specification, never the output of an adapter.

Run (no network, no model calls)::

    uv run --no-project \
        --with openai==1.83.0 --with anthropic==2.4.0 --with google-genai==1.63.0 \
        --with ollama==0.6.1 --with langchain-core==1.3.3 --with crewai==1.9.3 \
        python sdks/parity/adapters/generate.py [--check]

``--check`` fails when a committed file differs from what the script generates.

Case file format::

    adapter        corpus directory name
    case           file stem
    sdk            {"package": ..., "type": "module.Class"} the payload is an instance of
    construct      keyword arguments for the adapter constructor
    call           how the adapter is driven (kind is one of the CALL_* values below)
    payload        the serialized official object (``model_dump(mode="json")``) or record
    golden         the canonical trace; ``trace_id`` is the literal ``trc_normalized``

``call.kind``:

``trace_from_response``  provider adapters: ``adapter.trace_from_response(payload_object,
    input_messages=call.input_messages, started_at_ms=call.started_at_ms,
    ended_at_ms=call.ended_at_ms)``.
``callbacks``  LangChain: payload is ``{"events": [...]}``; each event is
    ``{"t_ms", "method", "run_id", "parent_run_id", "args"}``. ``run_id`` and
    ``parent_run_id`` are the run ids of the real run, renamed ``run-1``, ``run-2``... in
    order of first appearance (``parent_run_id`` is null for the root run), so runs that
    interleave are expressible. A replayer maps each ``run-N`` to one stable id and calls
    the handler with the clock (wall and monotonic) fixed at ``t_ms``, then
    ``build_trace()``. Values in ``args`` wrap non-JSON objects as
    ``{"$message": <message_to_dict>}``, ``{"$llm_result": ...}``,
    ``{"$error": {"type", "message"}}``.

    Python replays ``method(..., run_id=, parent_run_id=)`` as recorded. A langchainjs
    ``BaseCallbackHandler`` replayer maps:

    - ``on_chain_start``: ``handleChainStart({}, args.inputs, runId, parentRunId)``
    - ``on_chain_end``: ``handleChainEnd(args.outputs, runId, parentRunId)``
    - ``on_chat_model_start``: ``handleChatModelStart({}, args.messages, runId,
      parentRunId, args.invocation_params)``
    - ``on_llm_end``: ``handleLLMEnd(args.response, runId, parentRunId)``
    - ``on_tool_start``: ``handleToolStart({}, input, runId, parentRunId)`` with
      ``input = JSON.stringify(args.inputs)``: langchainjs takes a string, and
      ``args.input_str`` is Python's repr, so it is not used.
    - ``on_tool_end``: ``handleToolEnd(args.output, runId, parentRunId)``
    - ``on_tool_error``: ``handleToolError(new Error(message), runId, parentRunId)``

    ``$message`` / ``$llm_result`` values are decoded to the langchainjs message and
    ``LLMResult`` shapes (``{"generations": [[{"text", "message"}]], "llmOutput"}``).
``crew_output``  CrewAI: ``adapter.trace_from_crew_output(crew_output, crew)`` with no
    ``capture()`` window, so the trace has no latency.
``builder_ops``  ManualAdapter: payload is ``{"ops": [{"op", "args"}]}`` applied to the
    builder by method name.

Timestamps are fixed inputs (``started_at_ms``/``ended_at_ms`` or event ``t_ms``), so a
trace is deterministic; only ``trace_id`` is random and the checker replaces it.
"""

from __future__ import annotations

import argparse
import json
import sys
from importlib import metadata
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent
TRACE_ID = "trc_normalized"
AGENT = "corpus-agent"
INPUT_MESSAGES = [{"role": "user", "content": "Where is order ORD-1?"}]
STARTED, ENDED = 1000, 1250
CALL = {
    "kind": "trace_from_response",
    "input_messages": INPUT_MESSAGES,
    "started_at_ms": STARTED,
    "ended_at_ms": ENDED,
}

Case = dict[str, Any]

# Import root of each official SDK -> its pinned distribution name in versions.json.
DISTRIBUTIONS = {
    "openai": "openai",
    "anthropic": "anthropic",
    "google": "google-genai",
    "ollama": "ollama",
}


def _check_versions() -> dict[str, str]:
    pins: dict[str, str] = json.loads((ROOT / "versions.json").read_text())["packages"]
    wrong = []
    for package, pinned in pins.items():
        try:
            installed = metadata.version(package)
        except metadata.PackageNotFoundError:
            continue
        if installed != pinned:
            wrong.append(f"{package}: installed {installed}, pinned {pinned}")
    if wrong:
        raise SystemExit("versions.json pins differ from the installed SDKs:\n" + "\n".join(wrong))
    return pins


def _provider(
    adapter: str,
    case: str,
    description: str,
    sdk_type: str,
    obj: Any,
    golden: dict[str, Any],
    construct: dict[str, Any] | None = None,
    wire_nulls: bool = False,
) -> Case:
    # ``by_alias`` writes the names the wire (and the JS SDKs) use, e.g. Gemini's
    # ``functionCall``; ``model_validate`` accepts them back. ``wire_nulls`` keeps the
    # explicit nulls the provider sends (OpenAI ``content: null``); others omit them.
    package = DISTRIBUTIONS[sdk_type.split(".")[0]]
    dump = {"exclude_unset": True} if wire_nulls else {"exclude_none": True}
    return {
        "adapter": adapter,
        "case": case,
        "description": description,
        "sdk": {"package": package, "type": sdk_type},
        "construct": construct or {},
        "call": CALL,
        "payload": obj.model_dump(mode="json", by_alias=True, **dump),
        "golden": golden,
    }


def _golden(
    steps: list[dict[str, Any]],
    output: dict[str, Any],
    metadata_: dict[str, Any],
    input_: dict[str, Any] | None = None,
    agent_id: str | None = None,
) -> dict[str, Any]:
    golden: dict[str, Any] = {"schema_version": 1, "trace_id": TRACE_ID}
    if agent_id is not None:
        golden["agent_id"] = agent_id
    golden["input"] = input_ if input_ is not None else {"messages": INPUT_MESSAGES}
    golden["steps"] = steps
    golden["output"] = output
    golden["metadata"] = metadata_
    return golden


def _llm(model: str | None, result: dict[str, Any]) -> dict[str, Any]:
    return {
        "type": "llm_call",
        "name": "completion",
        "args": {"model": model} if model else {},
        "result": result,
        "started_at_ms": STARTED,
        "ended_at_ms": ENDED,
    }


def _tool(name: str, args: dict[str, Any]) -> dict[str, Any]:
    return {"type": "tool_call", "name": name, "args": args}


# --------------------------------------------------------------------------- OpenAI


def openai_cases() -> list[Case]:
    from openai.types import CompletionUsage
    from openai.types.chat import ChatCompletion, ChatCompletionMessage
    from openai.types.chat.chat_completion import Choice
    from openai.types.chat.chat_completion_message_tool_call import (
        ChatCompletionMessageToolCall,
        Function,
    )
    from openai.types.responses import (
        Response,
        ResponseFunctionToolCall,
        ResponseOutputMessage,
        ResponseOutputRefusal,
        ResponseOutputText,
        ResponseUsage,
    )
    from openai.types.responses.response_usage import (
        InputTokensDetails,
        OutputTokensDetails,
    )

    def provider(*args: Any) -> Case:
        return _provider(*args, wire_nulls=True)

    chat_type = "openai.types.chat.ChatCompletion"
    responses_type = "openai.types.responses.Response"
    chat_model = "gpt-4.1-2025-04-14"
    refusal = "I can't help with that request."
    usage = CompletionUsage(prompt_tokens=12, completion_tokens=8, total_tokens=20)
    split = {"tokens": 20, "input_tokens": 12, "output_tokens": 8}
    meta = {"total_tokens": 20, "latency_ms": 250, "model": chat_model}

    def chat(message: ChatCompletionMessage, finish: str) -> ChatCompletion:
        return ChatCompletion(
            id="chatcmpl-corpus",
            object="chat.completion",
            created=1,
            model=chat_model,
            choices=[Choice(index=0, finish_reason=finish, logprobs=None, message=message)],  # type: ignore[arg-type]
            usage=usage,
        )

    def response(output: list[Any]) -> Response:
        return Response(
            id="resp_corpus",
            object="response",
            created_at=1.0,
            model=chat_model,
            output=output,
            parallel_tool_calls=True,
            tool_choice="auto",
            tools=[],
            usage=ResponseUsage(
                input_tokens=12,
                output_tokens=8,
                total_tokens=20,
                input_tokens_details=InputTokensDetails(cached_tokens=0),
                output_tokens_details=OutputTokensDetails(reasoning_tokens=0),
            ),
        )

    def text_message(part: Any) -> ResponseOutputMessage:
        return ResponseOutputMessage(
            id="msg_corpus", type="message", role="assistant", status="completed", content=[part]
        )

    call = ResponseFunctionToolCall(
        id="fc_corpus",
        type="function_call",
        call_id="call_corpus",
        name="lookup_order",
        arguments='{"order_id": "ORD-1"}',
        status="completed",
    )
    structured = {"structured": {}}
    return [
        provider(
            "openai",
            "chat_text",
            "Chat Completions text answer with usage.",
            chat_type,
            chat(ChatCompletionMessage(
                    role="assistant", content="Order ORD-1 shipped.", refusal=None
                ), "stop"),
            _golden(
                [_llm(chat_model, {"completion": "Order ORD-1 shipped.", **split})],
                {"message": "Order ORD-1 shipped.", **structured},
                meta,
            ),
        ),
        provider(
            "openai",
            "chat_tool_call",
            "Chat Completions function call; arguments arrive as a JSON string.",
            chat_type,
            chat(
                ChatCompletionMessage(
                    role="assistant",
                    content=None,
                    refusal=None,
                    tool_calls=[
                        ChatCompletionMessageToolCall(
                            id="call_corpus",
                            type="function",
                            function=Function(
                                name="lookup_order", arguments='{"order_id": "ORD-1"}'
                            ),
                        )
                    ],
                ),
                "tool_calls",
            ),
            _golden(
                [_llm(chat_model, {"completion": "", **split}), _tool("lookup_order", {"order_id": "ORD-1"})],
                {"message": "", **structured},
                meta,
            ),
        ),
        provider(
            "openai",
            "chat_refusal",
            "Chat Completions refusal: the refusal text is the output, not an empty message.",
            chat_type,
            chat(ChatCompletionMessage(role="assistant", content=None, refusal=refusal), "stop"),
            _golden(
                [_llm(chat_model, {"completion": refusal, "refusal": refusal, **split})],
                {"message": refusal, **structured},
                meta,
            ),
        ),
        provider(
            "openai",
            "responses_text",
            "Responses API text answer; the object has output items, not choices.",
            responses_type,
            response(
                [
                    text_message(
                        ResponseOutputText(
                            type="output_text", text="Order ORD-1 shipped.", annotations=[]
                        )
                    )
                ]
            ),
            _golden(
                [_llm(chat_model, {"completion": "Order ORD-1 shipped.", **split})],
                {"message": "Order ORD-1 shipped.", **structured},
                meta,
            ),
        ),
        provider(
            "openai",
            "responses_tool_call",
            "Responses API function_call output item.",
            responses_type,
            response([call]),
            _golden(
                [_llm(chat_model, {"completion": "", **split}), _tool("lookup_order", {"order_id": "ORD-1"})],
                {"message": "", **structured},
                meta,
            ),
        ),
        provider(
            "openai",
            "responses_refusal",
            "Responses API refusal content part.",
            responses_type,
            response([text_message(ResponseOutputRefusal(type="refusal", refusal=refusal))]),
            _golden(
                [_llm(chat_model, {"completion": refusal, "refusal": refusal, **split})],
                {"message": refusal, **structured},
                meta,
            ),
        ),
    ]


# ------------------------------------------------------------------------- Anthropic


def anthropic_cases() -> list[Case]:
    from anthropic.types import (
        Message,
        ServerToolUseBlock,
        TextBlock,
        ToolUseBlock,
        Usage,
        WebSearchResultBlock,
        WebSearchToolResultBlock,
    )

    model = "claude-sonnet-4-5-20250929"
    split = {"tokens": 30, "input_tokens": 21, "output_tokens": 9}
    meta = {"total_tokens": 30, "latency_ms": 250, "model": model}

    def message(content: list[Any], stop: str) -> Message:
        return Message(
            id="msg_corpus",
            type="message",
            role="assistant",
            model=model,
            content=content,
            stop_reason=stop,  # type: ignore[arg-type]
            usage=Usage(input_tokens=21, output_tokens=9),
        )

    sdk = "anthropic.types.Message"
    return [
        _provider(
            "anthropic",
            "text",
            "Messages API text answer with usage.",
            sdk,
            message([TextBlock(type="text", text="Order ORD-1 shipped.")], "end_turn"),
            _golden(
                [_llm(model, {"completion": "Order ORD-1 shipped.", **split})],
                {"message": "Order ORD-1 shipped."},
                meta,
            ),
        ),
        _provider(
            "anthropic",
            "tool_use",
            "Client tool_use block.",
            sdk,
            message(
                [
                    ToolUseBlock(
                        type="tool_use",
                        id="toolu_corpus",
                        name="lookup_order",
                        input={"order_id": "ORD-1"},
                    )
                ],
                "tool_use",
            ),
            _golden(
                [_llm(model, {"completion": "", **split}), _tool("lookup_order", {"order_id": "ORD-1"})],
                {"message": ""},
                meta,
            ),
        ),
        _provider(
            "anthropic",
            "server_tool_use",
            "Server-side web_search: a server_tool_use block is a tool call.",
            sdk,
            message(
                [
                    TextBlock(type="text", text="Searching."),
                    ServerToolUseBlock(
                        type="server_tool_use",
                        id="srvtoolu_corpus",
                        name="web_search",
                        input={"query": "order ORD-1 status"},
                    ),
                    WebSearchToolResultBlock(
                        type="web_search_tool_result",
                        tool_use_id="srvtoolu_corpus",
                        content=[
                            WebSearchResultBlock(
                                type="web_search_result",
                                title="Order ORD-1",
                                url="https://example.com/orders/ORD-1",
                                encrypted_content="enc",
                            )
                        ],
                    ),
                    TextBlock(type="text", text="Order ORD-1 shipped."),
                ],
                "end_turn",
            ),
            _golden(
                [
                    _llm(model, {"completion": "Searching.\nOrder ORD-1 shipped.", **split}),
                    _tool("web_search", {"query": "order ORD-1 status"}),
                ],
                {"message": "Searching.\nOrder ORD-1 shipped."},
                meta,
            ),
        ),
    ]


# --------------------------------------------------------------------------- Gemini


def gemini_cases() -> list[Case]:
    from google.genai import types

    model = "gemini-2.0-flash-001"
    usage = types.GenerateContentResponseUsageMetadata(
        prompt_token_count=11, candidates_token_count=7, total_token_count=18
    )
    split = {"tokens": 18, "input_tokens": 11, "output_tokens": 7}
    meta = {"total_tokens": 18, "latency_ms": 250, "model": model}
    gemini_input = {"text": "Where is order ORD-1?"}

    def response(parts: list[Any]) -> Any:
        return types.GenerateContentResponse(
            candidates=[
                types.Candidate(
                    content=types.Content(role="model", parts=parts),
                    finish_reason=types.FinishReason.STOP,
                )
            ],
            usage_metadata=usage,
            model_version=model,
        )

    sdk = "google.genai.types.GenerateContentResponse"
    return [
        _provider(
            "gemini",
            "text_usage",
            "Text answer; the model name and token counts come from the response.",
            sdk,
            response([types.Part(text="Order ORD-1 shipped.")]),
            _golden(
                [_llm(model, {"completion": "Order ORD-1 shipped.", **split})],
                {"message": "Order ORD-1 shipped."},
                meta,
                input_=gemini_input,
            ),
        ),
        _provider(
            "gemini",
            "function_call",
            "function_call part with usage metadata.",
            sdk,
            response(
                [
                    types.Part(
                        function_call=types.FunctionCall(
                            name="lookup_order", args={"order_id": "ORD-1"}
                        )
                    )
                ]
            ),
            _golden(
                [_llm(model, {"completion": "", **split}), _tool("lookup_order", {"order_id": "ORD-1"})],
                {"message": ""},
                meta,
                input_=gemini_input,
            ),
        ),
        _provider(
            "gemini",
            "thought_function_call",
            "A thought-summary part beside a function_call: the reasoning is not the completion.",
            sdk,
            response(
                [
                    types.Part(text="I should look the order up first.", thought=True),
                    types.Part(
                        function_call=types.FunctionCall(
                            name="lookup_order", args={"order_id": "ORD-1"}
                        )
                    ),
                ]
            ),
            _golden(
                [_llm(model, {"completion": "", **split}), _tool("lookup_order", {"order_id": "ORD-1"})],
                {"message": ""},
                meta,
                input_=gemini_input,
            ),
        ),
    ]


# --------------------------------------------------------------------------- Ollama


def ollama_cases() -> list[Case]:
    import ollama

    model = "llama3.2"
    split = {"tokens": 15, "input_tokens": 9, "output_tokens": 6}
    meta = {"total_tokens": 15, "latency_ms": 250, "model": model}
    sdk = "ollama.ChatResponse"

    def response(message: Any) -> Any:
        return ollama.ChatResponse(
            model=model,
            created_at="2026-01-01T00:00:00Z",
            done=True,
            done_reason="stop",
            message=message,
            prompt_eval_count=9,
            eval_count=6,
        )

    return [
        _provider(
            "ollama",
            "text",
            "Chat answer with eval counts.",
            sdk,
            response(ollama.Message(role="assistant", content="Order ORD-1 shipped.")),
            _golden(
                [_llm(model, {"completion": "Order ORD-1 shipped.", **split})],
                {"message": "Order ORD-1 shipped."},
                meta,
            ),
        ),
        _provider(
            "ollama",
            "tool_call",
            "Native tool call with structured arguments.",
            sdk,
            response(
                ollama.Message(
                    role="assistant",
                    content="",
                    tool_calls=[
                        ollama.Message.ToolCall(
                            function=ollama.Message.ToolCall.Function(
                                name="lookup_order", arguments={"order_id": "ORD-1"}
                            )
                        )
                    ],
                )
            ),
            _golden(
                [_llm(model, {"completion": "", **split}), _tool("lookup_order", {"order_id": "ORD-1"})],
                {"message": ""},
                meta,
            ),
        ),
    ]


# ------------------------------------------------------------------------- LangChain


class _Recorder:
    """Records the callbacks a real LangChain run delivers to a handler."""

    def __init__(self) -> None:
        self.events: list[dict[str, Any]] = []
        self._ids: dict[str, str] = {}

    def _run(self, run_id: Any) -> str | None:
        """Random UUIDs become ``run-1``, ``run-2``... in order of first appearance."""
        if run_id is None:
            return None
        return self._ids.setdefault(str(run_id), f"run-{len(self._ids) + 1}")

    def record(self, method: str, run_id: Any, parent_run_id: Any, args: dict[str, Any]) -> None:
        self.events.append(
            {
                "t_ms": 1000 + 100 * len(self.events),
                "method": method,
                "run_id": self._run(run_id),
                "parent_run_id": self._run(parent_run_id),
                "args": args,
            }
        )


def _message(message: Any) -> dict[str, Any]:
    from langchain_core.messages import message_to_dict

    encoded = message_to_dict(message)
    if str(encoded["data"].get("id", "")).startswith("lc_run"):
        encoded["data"]["id"] = None  # random per run
    return encoded


def _encode(value: Any) -> Any:
    from langchain_core.messages import BaseMessage
    from langchain_core.outputs import LLMResult

    if isinstance(value, BaseMessage):
        return {"$message": _message(value)}
    if isinstance(value, LLMResult):
        return {
            "$llm_result": {
                "generations": [
                    [
                        {
                            "text": g.text,
                            "message": _message(g.message) if hasattr(g, "message") else None,
                        }
                        for g in row
                    ]
                    for row in value.generations
                ],
                "llm_output": value.llm_output,
            }
        }
    if isinstance(value, BaseException):
        return {"$error": {"type": type(value).__name__, "message": str(value)}}
    if isinstance(value, dict):
        return {str(k): _encode(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [_encode(v) for v in value]
    if isinstance(value, (str, int, float, bool)) or value is None:
        return value
    return str(value)


def _langchain_run(tool_mode: str) -> list[dict[str, Any]]:
    """Run a real LangChain agent loop on a fake chat model and record its callbacks."""
    from langchain_core.callbacks import BaseCallbackHandler
    from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
    from langchain_core.messages import AIMessage
    from langchain_core.runnables import RunnableConfig, RunnableLambda
    from langchain_core.tools import ToolException, tool

    recorder = _Recorder()

    class Handler(BaseCallbackHandler):
        def on_chain_start(self, serialized, inputs, *, run_id, parent_run_id=None, **kw):  # type: ignore[no-untyped-def]
            recorder.record("on_chain_start", run_id, parent_run_id, {"inputs": _encode(inputs)})

        def on_chain_end(self, outputs, *, run_id, parent_run_id=None, **kw):  # type: ignore[no-untyped-def]
            recorder.record("on_chain_end", run_id, parent_run_id, {"outputs": _encode(outputs)})

        def on_chat_model_start(self, serialized, messages, *, run_id, parent_run_id=None, **kw):  # type: ignore[no-untyped-def]
            recorder.record(
                "on_chat_model_start",
                run_id,
                parent_run_id,
                {
                    "messages": _encode(messages),
                    "invocation_params": kw.get("invocation_params"),
                },
            )

        def on_llm_end(self, response, *, run_id, parent_run_id=None, **kw):  # type: ignore[no-untyped-def]
            recorder.record("on_llm_end", run_id, parent_run_id, {"response": _encode(response)})

        def on_tool_start(self, serialized, input_str, *, run_id, parent_run_id=None, **kw):  # type: ignore[no-untyped-def]
            recorder.record(
                "on_tool_start",
                run_id,
                parent_run_id,
                {"serialized": serialized, "input_str": input_str, "inputs": _encode(kw.get("inputs"))},
            )

        def on_tool_end(self, output, *, run_id, parent_run_id=None, **kw):  # type: ignore[no-untyped-def]
            recorder.record("on_tool_end", run_id, parent_run_id, {"output": _encode(output)})

        def on_tool_error(self, error, *, run_id, parent_run_id=None, **kw):  # type: ignore[no-untyped-def]
            recorder.record("on_tool_error", run_id, parent_run_id, {"error": _encode(error)})

    fail_tool = tool_mode == "error"

    @tool
    def lookup_order(order_id: str) -> Any:
        """Look up an order."""
        if fail_tool:
            raise ToolException("order not found")
        if tool_mode == "blocks":
            return [{"type": "text", "text": "shipped"}]
        return "shipped"

    class FakeModel(GenericFakeChatModel):
        @property
        def _identifying_params(self) -> dict[str, Any]:
            return {"model_name": "gpt-4.1"}

    call = {"name": "lookup_order", "args": {"order_id": "ORD-1"}, "id": "call_corpus"}
    final = "Sorry, I could not find ORD-1." if fail_tool else "ORD-1 has shipped."
    model = FakeModel(
        messages=iter(
            [
                AIMessage(
                    content="",
                    tool_calls=[call],  # type: ignore[list-item]
                    usage_metadata={"input_tokens": 30, "output_tokens": 12, "total_tokens": 42},
                ),
                AIMessage(
                    content=final,
                    usage_metadata={"input_tokens": 55, "output_tokens": 6, "total_tokens": 61},
                ),
            ]
        )
    )

    def agent(inputs: dict[str, Any], config: RunnableConfig) -> dict[str, Any]:
        messages: list[Any] = [("user", inputs["input"])]
        ai = model.invoke(messages, config)
        try:
            tool_message = lookup_order.invoke(ai.tool_calls[0], config)
            messages += [ai, tool_message]
        except ToolException:
            messages += [ai]
        answer = model.invoke(messages, config)
        return {"output": answer.content}

    RunnableLambda(agent).invoke(
        {"input": "Where is ORD-1?"}, config={"callbacks": [Handler()]}  # type: ignore[arg-type]
    )
    return recorder.events


def langchain_cases() -> list[Case]:
    def lc_step_llm(name: str, started: int, ended: int, result: dict[str, Any]) -> dict[str, Any]:
        return {
            "type": "llm_call",
            "name": name,
            "args": {"model": name},
            "result": result,
            "metadata": {"duration_ms": ended - started},
            "started_at_ms": started,
            "ended_at_ms": ended,
            "agent_id": AGENT,
        }

    def lc_step_tool(
        args: dict[str, Any], result: dict[str, Any], started: int, ended: int
    ) -> dict[str, Any]:
        return {
            "type": "tool_call",
            "name": "lookup_order",
            "args": args,
            "result": result,
            "metadata": {"duration_ms": ended - started},
            "started_at_ms": started,
            "ended_at_ms": ended,
            "agent_id": AGENT,
        }

    cases = []
    for name, tool_mode, description, tool_result, answer in (
        (
            "tool_loop",
            "ok",
            "Agent loop: structured tool inputs, ToolMessage output, usage_metadata tokens, "
            "latency from the root chain's own end time.",
            {"output": "shipped"},
            "ORD-1 has shipped.",
        ),
        (
            "tool_error",
            "error",
            "A tool that raises ToolException: structured inputs and the error as result.",
            {"error": "order not found"},
            "Sorry, I could not find ORD-1.",
        ),
        (
            "tool_content_blocks",
            "blocks",
            "A tool that returns content blocks: the ToolMessage content is the block text, "
            "not the list's repr.",
            {"output": "shipped"},
            "ORD-1 has shipped.",
        ),
    ):
        fail = tool_mode == "error"
        # Event clock: chain 1000, llm 1100-1200, tool 1300-1400, llm 1500-1600, chain_end 1700.
        events = _langchain_run(tool_mode)
        names = [e["method"] for e in events]
        assert names == [
            "on_chain_start",
            "on_chat_model_start",
            "on_llm_end",
            "on_tool_start",
            "on_tool_error" if fail else "on_tool_end",
            "on_chat_model_start",
            "on_llm_end",
            "on_chain_end",
        ], names
        # Child chains (the runnable wrappers of each model/tool call) are not recorded
        # separately; only root callbacks reach the handler above.
        golden = {
            "schema_version": 1,
            "trace_id": TRACE_ID,
            "agent_id": AGENT,
            "input": {"message": "Where is ORD-1?"},
            "steps": [
                lc_step_llm(
                    "gpt-4.1", 1100, 1200, {"tokens": 42, "input_tokens": 30, "output_tokens": 12}
                ),
                lc_step_tool({"order_id": "ORD-1"}, tool_result, 1300, 1400),
                lc_step_llm(
                    "gpt-4.1",
                    1500,
                    1600,
                    {"completion": answer, "tokens": 61, "input_tokens": 55, "output_tokens": 6},
                ),
            ],
            "output": {"message": answer},
            "metadata": {"total_tokens": 103, "latency_ms": 700, "model": "gpt-4.1"},
        }
        cases.append(
            {
                "adapter": "langchain",
                "case": name,
                "description": description,
                "sdk": {"package": "langchain-core", "type": "langchain_core.messages.BaseMessage"},
                "construct": {"agent_id": AGENT},
                "call": {"kind": "callbacks"},
                "payload": {"events": events},
                "golden": golden,
            }
        )
    return cases


# --------------------------------------------------------------------------- CrewAI


def crewai_cases() -> list[Case]:
    from crewai import Agent, Crew, Task
    from crewai.crews.crew_output import CrewOutput
    from crewai.tasks.task_output import TaskOutput
    from crewai.types.usage_metrics import UsageMetrics

    output = CrewOutput(
        raw="Final report",
        tasks_output=[
            TaskOutput(description="Research the topic", raw="notes", agent="Researcher"),
            TaskOutput(description="Write the report", raw="Final report", agent="Writer"),
        ],
        token_usage=UsageMetrics(
            total_tokens=420, prompt_tokens=300, completion_tokens=120, successful_requests=3
        ),
    )
    agents = [
        Agent.model_construct(role=role, goal="g", backstory="b")
        for role in ("Researcher", "Writer", "Reviewer")
    ]
    crew = Crew.model_construct(
        agents=agents,
        tasks=[Task.model_construct(description="Research the topic", expected_output="notes")],
    )
    return [
        {
            "adapter": "crewai",
            "case": "summary_only",
            "description": (
                "Three agents are configured but only two tasks produced output. The trace "
                "is a run summary: no agent_call or tool_call steps are invented from "
                "configuration."
            ),
            "sdk": {"package": "crewai", "type": "crewai.crews.crew_output.CrewOutput"},
            "construct": {"agent_id": "corpus-crew"},
            "call": {"kind": "crew_output"},
            "payload": {
                "crew_output": output.model_dump(mode="json"),
                "crew": {
                    "agents": [a.model_dump(include={"role", "goal", "backstory"}) for a in agents],
                    "tasks": [
                        t.model_dump(include={"description", "expected_output"}) for t in crew.tasks
                    ],
                },
            },
            "golden": {
                "schema_version": 1,
                "trace_id": TRACE_ID,
                "agent_id": "corpus-crew",
                "steps": [],
                "output": {"message": "Final report"},
                "metadata": {"total_tokens": 420},
            },
        }
    ]


# --------------------------------------------------------------------------- Manual


def manual_cases() -> list[Case]:
    ops = [
        {"op": "set_input_dict", "args": {"input_data": {"question": "Where is ORD-1?"}}},
        {
            "op": "add_llm_call",
            "args": {
                "name": "plan",
                "args": {"model": "gpt-4.1"},
                "result": {"completion": "look it up", "tokens": 20},
                "started_at_ms": 1000,
                "ended_at_ms": 1100,
            },
        },
        {
            "op": "add_tool_call",
            "args": {
                "name": "lookup_order",
                "args": {"order_id": "ORD-1"},
                "result": {"status": "shipped"},
                "started_at_ms": 1100,
                "ended_at_ms": 1150,
            },
        },
        {"op": "set_output_dict", "args": {"output_data": {"message": "shipped"}}},
        {"op": "set_metadata", "args": {"total_tokens": 20, "latency_ms": 150, "model": "gpt-4.1"}},
    ]
    return [
        {
            "adapter": "manual",
            "case": "builder",
            "description": "ManualAdapter.capture applies builder calls verbatim.",
            "sdk": {"package": "attest-ai", "type": "attest.trace.TraceBuilder"},
            "construct": {"agent_id": AGENT},
            "call": {"kind": "builder_ops"},
            "payload": {"ops": ops},
            "golden": {
                "schema_version": 1,
                "trace_id": TRACE_ID,
                "agent_id": AGENT,
                "input": {"question": "Where is ORD-1?"},
                "steps": [
                    {
                        "type": "llm_call",
                        "name": "plan",
                        "args": {"model": "gpt-4.1"},
                        "result": {"completion": "look it up", "tokens": 20},
                        "started_at_ms": 1000,
                        "ended_at_ms": 1100,
                    },
                    {
                        "type": "tool_call",
                        "name": "lookup_order",
                        "args": {"order_id": "ORD-1"},
                        "result": {"status": "shipped"},
                        "started_at_ms": 1100,
                        "ended_at_ms": 1150,
                    },
                ],
                "output": {"message": "shipped"},
                "metadata": {"total_tokens": 20, "latency_ms": 150, "model": "gpt-4.1"},
            },
        }
    ]


GENERATORS = (
    openai_cases,
    anthropic_cases,
    gemini_cases,
    ollama_cases,
    langchain_cases,
    crewai_cases,
    manual_cases,
)


def render(case: Case) -> str:
    return json.dumps(case, indent=2, sort_keys=True) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--check", action="store_true", help="fail if committed files differ")
    args = parser.parse_args()
    _check_versions()

    expected: dict[Path, str] = {}
    for generate in GENERATORS:
        for case in generate():
            expected[ROOT / case["adapter"] / f"{case['case']}.json"] = render(case)

    if args.check:
        stale = [
            str(p.relative_to(ROOT))
            for p, text in expected.items()
            if not p.exists() or p.read_text() != text
        ]
        extra = [
            str(p.relative_to(ROOT))
            for p in ROOT.glob("*/*.json")
            if p not in expected
        ]
        if stale or extra:
            print("corpus out of date:", *stale, *[f"unexpected {e}" for e in extra], sep="\n  ")
            return 1
        return 0

    for path, text in expected.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
    print(f"wrote {len(expected)} cases")
    return 0


if __name__ == "__main__":
    sys.exit(main())
