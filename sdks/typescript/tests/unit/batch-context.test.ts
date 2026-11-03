import { describe, expect, it, vi } from "vitest";
import { AttestClient, zeroUsage, type EngineManager } from "../../packages/core/src/index.js";

describe("AttestClient.evaluateBatch context", () => {
  const trace = { trace_id: "trc_c", output: { message: "ok" }, steps: [] };

  function client() {
    const c = new AttestClient({} as EngineManager);
    const send = vi
      .spyOn(c, "sendRequest")
      .mockResolvedValue({ results: [], usage: zeroUsage() } as never);
    return { c, send };
  }

  it("sends the context only when given", async () => {
    const { c, send } = client();

    await c.evaluateBatch(trace, []);
    await c.evaluateBatch(trace, [], {
      context: { test_id: "tests/a.test.ts > adds up", file: "tests/a.test.ts", line: 12 },
    });

    expect(send.mock.calls[0]![1]).not.toHaveProperty("context");
    expect(send.mock.calls[1]![1]).toMatchObject({
      context: { test_id: "tests/a.test.ts > adds up", file: "tests/a.test.ts", line: 12 },
    });
  });
});
