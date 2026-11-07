# Harness for docs/guides/simulation.md (@fault_inject). `actual_api_call` is the reader's own call.
def actual_api_call(query):
    return query.upper()


# SNIPPET

outcomes = []
for _ in range(10):
    try:
        outcomes.append(flaky_api_call("q"))
    except RuntimeError as exc:
        outcomes.append(str(exc))
assert {"data": "Q"} in outcomes
assert "Injected fault in flaky_api_call" in outcomes
