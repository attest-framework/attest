# Attest

Test framework for AI agents with composable assertions across 8 layers.

## What is Attest?

Attest is a comprehensive testing framework for AI systems and agents. It provides a fluent, composable API for writing assertions at multiple levels — from schema validation to LLM-based evaluations. Write once, run anywhere: your tests work with OpenAI, Anthropic, Gemini, Ollama, and 10+ other integrations.

## Key Features

- **8-layer assertion stack**: Schema → constraints → trace → content → embedding → LLM judge → trace tree → simulation
- **Fluent DSL**: Chain assertions naturally: `expect(result).output_contains("...").cost_under(0.01).passes_judge("...")`
- **10+ adapters**: OpenAI, Anthropic, Gemini, Ollama, LangChain, Google ADK, LlamaIndex, OTel, CrewAI, Manual
- **Soft failures**: Continue running tests after assertion failures to detect multiple issues
- **Trace inspection**: Access full execution traces and reconstruct trace trees
- **Framework adapters**: Test agents built with LangChain, CrewAI, and more
- **LLM-as-judge**: Use powerful models to evaluate semantic correctness
- **Simulation runtime**: Run multi-agent scenarios with validation
- **Multi-language**: Python and TypeScript SDKs

## Quick Install

=== "Python"

    ```bash
    uv add attest-ai
    # or
    pip install attest-ai
    ```

=== "Node.js"

    ```bash
    npm install @attest-ai/core
    # or
    pnpm add @attest-ai/core
    ```

## Minimal Example

=== "Python"

    ```python
    from attest import expect

    # Your agent result
    result = agent.run("What is 2 + 2?")

    # Assert across layers
    (expect(result)
      .output_contains("4")
      .cost_under(0.05)
      .passes_judge("Is the answer mathematically correct?"))
    ```

=== "Node.js"

    ```typescript
    import { expect } from "@attest-ai/core";

    const result = await agent.run("What is 2 + 2?");

    expect(result)
      .output_contains("4")
      .cost_under(0.05)
      .passes_judge("Is the answer mathematically correct?");
    ```

## Getting Started

New to Attest? Start with the [quickstart guide](getting-started.md) to set up your first test in 5 minutes.

## Documentation

- **[Getting Started](getting-started.md)** — Installation, first test, run tests
- **[Python API Reference](reference/python/index.md)** — Expect DSL, adapters, decorators
- **[Migration Guides](migration/from-deepeval.md)** — Move from DeepEval, PromptFoo, or manual testing
- **[Tutorials](tutorials/writing-a-framework-adapter.md)** — Write custom adapters

## Use Cases

- **Agent testing**: Validate agent outputs, cost, latency, and semantic correctness
- **Regression detection**: Catch unexpected behavior changes across model updates
- **Quality gates**: Fail CI/CD if outputs violate assertions
- **A/B testing**: Compare agent configurations with the same test suite
- **Compliance**: Ensure outputs meet security and content policies

## Community

- GitHub: [attest-frameowrk/attest](https://github.com/attest-frameowrk/attest)
- Issues: [Report bugs or request features](https://github.com/attest-frameowrk/attest/issues)
- Discussions: [Ask questions and share ideas](https://github.com/attest-frameowrk/attest/discussions)

---

**Latest Release:** v0.3.0 — Simulation runtime, multi-agent testing, TypeScript SDK
