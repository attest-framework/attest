# Harness for docs/guides/simulation.md (@scenario). `my_agent` is the reader's agent; here it is offline.
from attest import AgentResult, TraceBuilder


class _Agent:
    def run(self, persona):
        trace = TraceBuilder("support-agent").set_output(message=f"hello from {persona.name}").build()
        return AgentResult(trace=trace)


my_agent = _Agent()

# SNIPPET

# seed=42 injects no fault in five turns, so every turn reaches the agent.
outcome = test_multi_turn_support()
assert len(outcome.results) == 5, len(outcome.results)
assert outcome.results[0].trace.output["message"] == "hello from friendly_user"
