# SNIPPET

from types import SimpleNamespace

tool_use = SimpleNamespace(tool_name="lookup", args={"q": "x"}, result={"ok": True}, tokens=3)
task = SimpleNamespace(agent="researcher", raw="done", tool_uses=[tool_use])
trace = CrewAIAdapter(agent_id="crew").trace_from_crew(SimpleNamespace(tasks_output=[task], raw="final"))
assert trace.output["message"] == "final"
assert [s.type for s in trace.steps] == ["llm_call", "tool_call"], trace.steps
