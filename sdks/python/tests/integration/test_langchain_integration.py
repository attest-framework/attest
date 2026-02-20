"""Integration tests for LangChain adapter with real framework objects."""

from __future__ import annotations

from uuid import uuid4

import pytest

lc_core = pytest.importorskip("langchain_core")

from langchain_core.messages import HumanMessage  # noqa: E402
from langchain_core.outputs import Generation, LLMResult  # noqa: E402

from attest.adapters.langchain import LangChainCallbackHandler  # noqa: E402


pytestmark = pytest.mark.integration


class TestLangChainCallbackHandlerIntegration:
    """Tests using real langchain_core objects against the Attest adapter."""

    def test_handler_has_required_callback_methods(self) -> None:
        """LangChainCallbackHandler implements all BaseCallbackHandler methods used by LangChain."""
        handler = LangChainCallbackHandler(agent_id="test-agent")
        # Verify the handler exposes the callback interface LangChain expects
        assert callable(getattr(handler, "on_chain_start", None))
        assert callable(getattr(handler, "on_chain_end", None))
        assert callable(getattr(handler, "on_chat_model_start", None))
        assert callable(getattr(handler, "on_llm_end", None))
        assert callable(getattr(handler, "on_tool_start", None))
        assert callable(getattr(handler, "on_tool_end", None))
        assert callable(getattr(handler, "on_tool_error", None))

    def test_real_llm_result_produces_trace(self) -> None:
        """Real LLMResult with token usage flows through to trace metadata."""
        handler = LangChainCallbackHandler(agent_id="lc-agent")
        run_id = uuid4()

        handler.on_chat_model_start(
            serialized={"name": "ChatOpenAI"},
            messages=[[HumanMessage(content="What is the capital of France?")]],
            run_id=run_id,
            invocation_params={"model_name": "gpt-4.1"},
        )

        result = LLMResult(
            generations=[[Generation(text="Paris")]],
            llm_output={
                "token_usage": {
                    "prompt_tokens": 10,
                    "completion_tokens": 5,
                    "total_tokens": 15,
                },
                "model_name": "gpt-4.1",
            },
        )
        handler.on_llm_end(response=result, run_id=run_id)

        trace = handler.build_trace()
        assert trace.steps, "trace has at least one step"

        llm_step = trace.steps[0]
        assert llm_step.type == "llm_call"
        assert llm_step.result is not None
        assert llm_step.result["completion"] == "Paris"
        assert llm_step.result["input_tokens"] == 10
        assert llm_step.result["output_tokens"] == 5

    def test_real_human_message_as_chain_input(self) -> None:
        """Real HumanMessage passed as chain input extracts content."""
        handler = LangChainCallbackHandler(agent_id="lc-chain")
        run_id = uuid4()

        handler.on_chain_start(
            serialized={"name": "AgentExecutor"},
            inputs={"messages": [HumanMessage(content="Hello")]},
            run_id=run_id,
            parent_run_id=None,
        )
        handler.on_chain_end(
            outputs={"output": "Hi there!"},
            run_id=run_id,
            parent_run_id=None,
        )

        trace = handler.build_trace()
        assert trace.input is not None
        assert trace.input["message"] == "Hello"

    def test_real_generation_text_extraction(self) -> None:
        """Real Generation object text flows through to step result completion."""
        handler = LangChainCallbackHandler(agent_id="lc-gen")
        run_id = uuid4()

        handler.on_chat_model_start(
            serialized={"name": "ChatModel"},
            messages=[[HumanMessage(content="test")]],
            run_id=run_id,
        )

        generation = Generation(text="This is the model response")
        result = LLMResult(generations=[[generation]])
        handler.on_llm_end(response=result, run_id=run_id)

        trace = handler.build_trace()
        llm_step = trace.steps[0]
        assert llm_step.result is not None
        assert llm_step.result["completion"] == "This is the model response"
