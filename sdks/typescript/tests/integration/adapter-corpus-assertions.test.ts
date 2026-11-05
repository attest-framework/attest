import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { existsSync } from "node:fs";
import { AttestClient, EngineManager, attestExpect } from "../../packages/core/src/index.js";
import type { Trace } from "../../packages/core/src/proto/types.js";
import { ENGINE_PATH, caseId, loadCases, produce } from "../support/adapter-conformance.js";

const ORDER_SCHEMA = {
  type: "object",
  properties: { order_id: { type: "string" } },
  required: ["order_id"],
  additionalProperties: false,
};

const CASES = new Map(loadCases().map((c) => [caseId(c), c]));

function trace(name: string): Trace {
  const c = CASES.get(name);
  if (c === undefined) throw new Error(`no corpus case ${name}`);
  return produce(c);
}

describe("assertions on adapter corpus traces", () => {
  let engine: EngineManager;
  let client: AttestClient;

  beforeAll(async () => {
    expect(existsSync(ENGINE_PATH), `build the engine first: make engine (${ENGINE_PATH})`).toBe(true);
    engine = new EngineManager({ enginePath: ENGINE_PATH });
    await engine.start();
    client = new AttestClient(engine);
  });

  afterAll(async () => {
    await engine?.stop();
  });

  for (const name of ["openai/chat_tool_call", "openai/responses_tool_call", "langchain/tool_loop"]) {
    it(`toolArgsMatchSchema passes on ${name}`, async () => {
      const t = trace(name);
      const chain = attestExpect(t).toolArgsMatchSchema("lookup_order", ORDER_SCHEMA);

      const result = await client.evaluateBatch(t, chain.assertions);

      expect(result.results.map((r) => [r.status, r.explanation])).toEqual([["pass", expect.any(String)]]);
    });
  }

  for (const name of ["openai/chat_refusal", "openai/responses_refusal"]) {
    it(`outputNotContains fails on the refusal text of ${name}`, async () => {
      const t = trace(name);
      const chain = attestExpect(t).outputNotContains("can't");

      const result = await client.evaluateBatch(t, chain.assertions);

      expect(result.results.map((r) => r.status)).toEqual(["hard_fail"]);
    });
  }
});
