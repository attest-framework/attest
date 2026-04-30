"""Tests for BaseAdapter and BaseProviderAdapter ABC hierarchy."""

from __future__ import annotations

import warnings
from typing import Any
from unittest.mock import patch

import pytest

from attest.adapters._base import BaseAdapter, BaseProviderAdapter


class TestBaseAdapter:
    """Tests for BaseAdapter shared utilities."""

    def test_instantiable_as_base(self) -> None:
        """BaseAdapter can be instantiated (it has no abstract methods itself)."""
        adapter = BaseAdapter(agent_id="test")
        assert adapter._agent_id == "test"

    def test_create_builder_uses_agent_id(self) -> None:
        """_create_builder passes the adapter's agent_id to TraceBuilder."""
        adapter = BaseAdapter(agent_id="test-agent")
        builder = adapter._create_builder()
        assert builder._agent_id == "test-agent"

    def test_create_builder_none_agent_id(self) -> None:
        """_create_builder works with no agent_id."""
        adapter = BaseAdapter()
        builder = adapter._create_builder()
        assert builder._agent_id is None

    def test_now_ms_returns_int(self) -> None:
        """_now_ms returns an integer timestamp in milliseconds."""
        adapter = BaseAdapter()
        result = adapter._now_ms()
        assert isinstance(result, int)
        assert result > 1_700_000_000_000  # After 2023

    def test_resolve_timestamps_passthrough(self) -> None:
        """_resolve_timestamps returns provided values when both are set."""
        adapter = BaseAdapter()
        started, ended = adapter._resolve_timestamps(100, 200)
        assert started == 100
        assert ended == 200

    def test_resolve_timestamps_fallback_to_now(self) -> None:
        """_resolve_timestamps falls back to current time when None."""
        adapter = BaseAdapter()
        with patch.object(adapter, "_now_ms", return_value=999):
            started, ended = adapter._resolve_timestamps(None, None)
        assert started == 999
        assert ended == 999

    def test_resolve_timestamps_partial_fallback(self) -> None:
        """_resolve_timestamps only fills in None values."""
        adapter = BaseAdapter()
        with patch.object(adapter, "_now_ms", return_value=999):
            started, ended = adapter._resolve_timestamps(100, None)
        assert started == 100
        assert ended == 999


class TestBaseProviderAdapter:
    """Tests for BaseProviderAdapter template method."""

    def test_cannot_instantiate_directly(self) -> None:
        """BaseProviderAdapter is abstract — requires all extraction methods."""
        with pytest.raises(TypeError):
            BaseProviderAdapter()  # type: ignore[abstract]

    def test_missing_abstract_method_raises(self) -> None:
        """Subclass missing any abstract method raises TypeError."""

        class Incomplete(BaseProviderAdapter):
            def _extract_completion(self, response: Any) -> str:
                return ""

            def _extract_model(self, response: Any, **metadata: Any) -> str | None:
                return None

            # Missing: _extract_total_tokens, _extract_tool_calls

        with pytest.raises(TypeError):
            Incomplete()  # type: ignore[abstract]

    def test_complete_subclass_works(self) -> None:
        """Subclass implementing all abstract methods can be instantiated."""

        class Complete(BaseProviderAdapter):
            def _extract_completion(self, response: Any) -> str:
                return response.get("text", "")

            def _extract_model(self, response: Any, **metadata: Any) -> str | None:
                return response.get("model")

            def _extract_total_tokens(self, response: Any) -> int | None:
                return response.get("tokens")

            def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
                return []

        adapter = Complete(agent_id="test")
        response = {"text": "hello", "model": "gpt-4", "tokens": 42}
        trace = adapter.trace_from_response(response)

        assert trace.output is not None
        assert trace.output["message"] == "hello"
        assert trace.metadata is not None
        assert trace.metadata.model == "gpt-4"
        assert trace.metadata.total_tokens == 42
        assert len(trace.steps) == 1
        assert trace.steps[0].type == "llm_call"
        assert trace.steps[0].args is not None
        assert trace.steps[0].args["model"] == "gpt-4"

    def test_tool_calls_added_as_steps(self) -> None:
        """Tool calls from _extract_tool_calls become tool_call steps."""

        class WithTools(BaseProviderAdapter):
            def _extract_completion(self, response: Any) -> str:
                return "done"

            def _extract_model(self, response: Any, **metadata: Any) -> str | None:
                return None

            def _extract_total_tokens(self, response: Any) -> int | None:
                return None

            def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
                return [
                    {"name": "search", "args": {"query": "test"}},
                    {"name": "calculate"},
                ]

        adapter = WithTools()
        trace = adapter.trace_from_response({})

        # 1 llm_call + 2 tool_calls
        assert len(trace.steps) == 3
        assert trace.steps[1].type == "tool_call"
        assert trace.steps[1].name == "search"
        assert trace.steps[1].args == {"query": "test"}
        assert trace.steps[2].type == "tool_call"
        assert trace.steps[2].name == "calculate"

    def test_input_messages_set(self) -> None:
        """input_messages are set on the trace input."""

        class Simple(BaseProviderAdapter):
            def _extract_completion(self, response: Any) -> str:
                return ""

            def _extract_model(self, response: Any, **metadata: Any) -> str | None:
                return None

            def _extract_total_tokens(self, response: Any) -> int | None:
                return None

            def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
                return []

        adapter = Simple()
        messages = [{"role": "user", "content": "hi"}]
        trace = adapter.trace_from_response({}, input_messages=messages)

        assert trace.input is not None
        assert trace.input["messages"] == messages

    def test_metadata_passed_through(self) -> None:
        """cost_usd and latency_ms metadata are passed to the trace."""

        class Simple(BaseProviderAdapter):
            def _extract_completion(self, response: Any) -> str:
                return ""

            def _extract_model(self, response: Any, **metadata: Any) -> str | None:
                return None

            def _extract_total_tokens(self, response: Any) -> int | None:
                return None

            def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
                return []

        adapter = Simple()
        trace = adapter.trace_from_response({}, cost_usd=0.01, latency_ms=150)

        assert trace.metadata is not None
        assert trace.metadata.cost_usd == 0.01
        assert trace.metadata.latency_ms == 150

    def test_build_output_override(self) -> None:
        """Subclass can override _build_output for custom output format."""

        class CustomOutput(BaseProviderAdapter):
            def _extract_completion(self, response: Any) -> str:
                return "text"

            def _extract_model(self, response: Any, **metadata: Any) -> str | None:
                return None

            def _extract_total_tokens(self, response: Any) -> int | None:
                return None

            def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
                return []

            def _build_output(
                self, response: Any, completion_text: str, **metadata: Any
            ) -> dict[str, Any]:
                return {"message": completion_text, "custom": "field"}

        adapter = CustomOutput()
        trace = adapter.trace_from_response({})

        assert trace.output is not None
        assert trace.output["custom"] == "field"


class TestGeminiDeprecation:
    """Test Gemini input_text backward compatibility."""

    def test_input_text_emits_deprecation_warning(self) -> None:
        """Using input_text kwarg emits DeprecationWarning."""
        from attest.adapters.gemini import GeminiAdapter

        adapter = GeminiAdapter()

        # Build a minimal mock response
        class MockPart:
            text = "response"

        class MockContent:
            parts = [MockPart()]

        class MockCandidate:
            content = MockContent()

        class MockResponse:
            text = "response"
            candidates = [MockCandidate()]

        with warnings.catch_warnings(record=True) as w:
            warnings.simplefilter("always")
            trace = adapter.trace_from_response(MockResponse(), input_text="hello")

        assert len(w) == 1
        assert issubclass(w[0].category, DeprecationWarning)
        assert "input_text" in str(w[0].message)

        # Verify the input was still captured
        assert trace.input is not None
        assert trace.input["text"] == "hello"

    def test_input_messages_no_warning(self) -> None:
        """Using input_messages does not emit warning."""
        from attest.adapters.gemini import GeminiAdapter

        adapter = GeminiAdapter()

        class MockPart:
            text = "response"

        class MockContent:
            parts = [MockPart()]

        class MockCandidate:
            content = MockContent()

        class MockResponse:
            text = "response"
            candidates = [MockCandidate()]

        with warnings.catch_warnings(record=True) as w:
            warnings.simplefilter("always")
            adapter.trace_from_response(
                MockResponse(),
                input_messages=[{"role": "user", "content": "hello"}],
            )

        deprecation_warnings = [x for x in w if issubclass(x.category, DeprecationWarning)]
        assert len(deprecation_warnings) == 0


class TestAdapterHierarchy:
    """Verify the class hierarchy is correct."""

    def test_provider_adapters_inherit_base_provider(self) -> None:
        """All provider adapters inherit from BaseProviderAdapter."""
        from attest.adapters.anthropic import AnthropicAdapter
        from attest.adapters.gemini import GeminiAdapter
        from attest.adapters.ollama import OllamaAdapter
        from attest.adapters.openai import OpenAIAdapter

        assert issubclass(OpenAIAdapter, BaseProviderAdapter)
        assert issubclass(AnthropicAdapter, BaseProviderAdapter)
        assert issubclass(GeminiAdapter, BaseProviderAdapter)
        assert issubclass(OllamaAdapter, BaseProviderAdapter)

    def test_framework_adapters_inherit_base(self) -> None:
        """All framework adapters inherit from BaseAdapter."""
        from attest.adapters.google_adk import GoogleADKAdapter
        from attest.adapters.langchain import LangChainAdapter, LangChainCallbackHandler
        from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler
        from attest.adapters.manual import ManualAdapter
        from attest.adapters.otel import OTelAdapter

        assert issubclass(GoogleADKAdapter, BaseAdapter)
        assert issubclass(OTelAdapter, BaseAdapter)
        assert issubclass(ManualAdapter, BaseAdapter)
        assert issubclass(LlamaIndexInstrumentationHandler, BaseAdapter)
        assert issubclass(LangChainCallbackHandler, BaseAdapter)
        assert issubclass(LangChainAdapter, BaseAdapter)

    def test_base_provider_inherits_base(self) -> None:
        """BaseProviderAdapter inherits from BaseAdapter."""
        assert issubclass(BaseProviderAdapter, BaseAdapter)

    def test_exports_accessible(self) -> None:
        """BaseAdapter and BaseProviderAdapter are accessible from top-level."""
        import attest as attest_pkg
        from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

        assert attest_pkg.BaseAdapter is BaseAdapter
        assert attest_pkg.BaseProviderAdapter is BaseProviderAdapter
        assert attest_pkg.LlamaIndexInstrumentationHandler is LlamaIndexInstrumentationHandler
