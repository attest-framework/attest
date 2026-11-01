import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { existsSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  EngineManager,
  AttestClient,
  TraceBuilder,
  attestExpect,
  AgentResult,
  STEP_LLM_CALL,
  STEP_TOOL_CALL,
} from "../../packages/core/src/index.js";

describe("Engine Integration", () => {
  let engine: EngineManager | undefined;
  let client: AttestClient | undefined;
  let skipReason: string | undefined;

  beforeAll(async () => {
    // The engine built by `make engine`; never auto-download from a test run.
    const enginePath = resolve(
      dirname(fileURLToPath(import.meta.url)),
      "../../../../bin/attest-engine",
    );
    if (!existsSync(enginePath)) {
      skipReason = "attest-engine binary not found; build with 'make engine'";
      return;
    }
    // Engine-spawning tests must not pick up a developer's judge configuration.
    for (const key of ["OPENAI_API_KEY", "ATTEST_JUDGE_API_KEY", "ATTEST_JUDGE_BASE_URL"]) {
      delete process.env[key];
    }
    try {
      engine = new EngineManager({ enginePath });
      const initResult = await engine.start();
      expect(initResult.compatible).toBe(true);
      client = new AttestClient(engine);
    } catch (err) {
      skipReason =
        err instanceof Error ? err.message : "Engine binary not available";
    }
  });

  afterAll(async () => {
    if (engine) {
      await engine.stop();
    }
  });

  it("evaluates a basic schema assertion", async (ctx) => {
    if (skipReason || !client || !engine) {
      ctx.skip();
      return;
    }

    const trace = new TraceBuilder("test-agent")
      .setInput({ query: "What is the capital of France?" })
      .addLlmCall("gpt-4", {
        args: { model: "gpt-4" },
        result: { completion: "Paris is the capital of France." },
      })
      .setOutput({
        message: "Paris is the capital of France.",
        structured: { answer: "Paris", confidence: 0.95 },
      })
      .setMetadata({ total_tokens: 100, cost_usd: 0.003, latency_ms: 500 })
      .build();

    const result = new AgentResult(trace);
    const chain = attestExpect(result)
      .outputContains("Paris")
      .costUnder(0.01)
      .tokensUnder(500);

    const evalResult = await client.evaluateBatch(trace, chain.assertions);
    expect(evalResult.results).toHaveLength(3);
    for (const r of evalResult.results) {
      expect(r.status).toBe("pass");
    }
  });

  it("evaluates trace assertions", async (ctx) => {
    if (skipReason || !client || !engine) {
      ctx.skip();
      return;
    }

    const trace = new TraceBuilder("tool-agent")
      .setInput({ query: "Search for TypeScript docs" })
      .addToolCall("search", {
        args: { q: "TypeScript docs" },
        result: { results: ["doc1", "doc2"] },
      })
      .addLlmCall("gpt-4", {
        result: { completion: "Found TypeScript documentation." },
      })
      .setOutput({ message: "Found TypeScript documentation." })
      .build();

    const result = new AgentResult(trace);
    const chain = attestExpect(result)
      .toolsCalledInOrder(["search", "gpt-4"])
      .requiredTools(["search"]);

    const evalResult = await client.evaluateBatch(trace, chain.assertions);
    expect(evalResult.results).toHaveLength(2);
  });
});
