# SNIPPET

from types import SimpleNamespace

response = SimpleNamespace(
    model="mistral-small",
    choices=[SimpleNamespace(message=SimpleNamespace(content="hello", tool_calls=[
        SimpleNamespace(function=SimpleNamespace(name="lookup", arguments="{}"))
    ]))],
    usage=SimpleNamespace(total_tokens=7),
)
trace = MistralAdapter(agent_id="mistral-agent").trace_from_response(
    response, input_messages=[{"role": "user", "content": "Hi"}]
)
assert trace.output["message"] == "hello"
assert [s.type for s in trace.steps] == ["llm_call", "tool_call"], trace.steps
