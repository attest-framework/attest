"""Tests for Ollama adapter."""

from __future__ import annotations

from attest.adapters.ollama import OllamaAdapter


def _make_response(
    content: str = "Ollama reply.",
    model: str = "llama3.2",
    eval_count: int = 80,
    prompt_eval_count: int = 20,
) -> dict:  # type: ignore[type-arg]  # plain dict matches Ollama wire format
    return {
        "model": model,
        "message": {"role": "assistant", "content": content},
        "eval_count": eval_count,
        "prompt_eval_count": prompt_eval_count,
        "done": True,
    }


def test_ollama_basic_response() -> None:
    adapter = OllamaAdapter(agent_id="ollama-agent")
    response = _make_response()
    trace = adapter.trace_from_response(
        response, input_messages=[{"role": "user", "content": "hello"}]
    )
    assert trace.agent_id == "ollama-agent"
    assert trace.output["message"] == "Ollama reply."
    assert trace.input == {"messages": [{"role": "user", "content": "hello"}]}


def test_ollama_llm_step_captured() -> None:
    adapter = OllamaAdapter()
    response = _make_response(model="mistral")
    trace = adapter.trace_from_response(response)
    llm_steps = [s for s in trace.steps if s.type == "llm_call"]
    assert len(llm_steps) == 1
    assert llm_steps[0].args == {"model": "mistral"}


def test_ollama_token_count() -> None:
    adapter = OllamaAdapter()
    response = _make_response(eval_count=60, prompt_eval_count=40)
    trace = adapter.trace_from_response(response)
    assert trace.metadata is not None
    assert trace.metadata.total_tokens == 100  # 60 + 40


def test_ollama_model_in_metadata() -> None:
    adapter = OllamaAdapter()
    response = _make_response(model="llama3.2")
    trace = adapter.trace_from_response(response)
    assert trace.metadata is not None
    assert trace.metadata.model == "llama3.2"


def test_ollama_latency_metadata() -> None:
    adapter = OllamaAdapter()
    response = _make_response()
    trace = adapter.trace_from_response(response, latency_ms=450)
    assert trace.metadata is not None
    assert trace.metadata.latency_ms == 450


def test_ollama_missing_token_counts() -> None:
    adapter = OllamaAdapter()
    response = {"model": "phi3", "message": {"role": "assistant", "content": "hi"}, "done": True}
    trace = adapter.trace_from_response(response)
    assert trace.metadata is not None
    assert trace.metadata.total_tokens is None


def test_ollama_no_input_messages() -> None:
    adapter = OllamaAdapter()
    response = _make_response()
    trace = adapter.trace_from_response(response)
    assert trace.input is None
