# Harness for docs/guides/simulation.md (@mock_tool).
# SNIPPET

assert mock_search("x") == {"results": ["Mock result for: x"]}
assert mock_search._mock_tool_name == "search_web"
