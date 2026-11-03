import { describe, expect, it, vi } from "vitest";
import {
  AttestClient,
  REASON_BUDGET_EXHAUSTED,
  REASON_JUDGE_ABSTAINED,
  addUsage,
  formatUsage,
  parseUsage,
  sumUsage,
  usageSummaryLine,
  zeroUsage,
  type EngineManager,
  type Usage,
} from "../../packages/core/src/index.js";

const wire: Usage = {
  model_calls: 3,
  judge_calls: 2,
  input_tokens: 1200,
  output_tokens: 300,
  cached_tokens: 400,
  cost_usd: 0.01234,
  retry_cost_usd: 0.00051,
  unpriced_calls: 0,
  budget_refused: 0,
};

describe("reason constants", () => {
  it("name the engine's budget and abstain reasons", () => {
    expect(REASON_BUDGET_EXHAUSTED).toBe("budget_exhausted");
    expect(REASON_JUDGE_ABSTAINED).toBe("judge_abstained");
  });
});

describe("parseUsage", () => {
  it("accepts the nine wire fields", () => {
    expect(parseUsage({ ...wire })).toEqual(wire);
  });

  it("rejects a missing usage, a missing field, a negative count and a fractional count", () => {
    expect(() => parseUsage(undefined)).toThrow("usage must be an object");
    const { judge_calls: _omitted, ...missing } = wire;
    expect(() => parseUsage(missing)).toThrow("usage.judge_calls must be a non-negative integer");
    expect(() => parseUsage({ ...wire, model_calls: -1 })).toThrow("usage.model_calls");
    expect(() => parseUsage({ ...wire, input_tokens: 1.5 })).toThrow("usage.input_tokens");
    expect(() => parseUsage({ ...wire, cost_usd: "0.1" })).toThrow("usage.cost_usd must be a non-negative number");
  });
});

describe("usage sums", () => {
  it("add field by field", () => {
    expect(addUsage(wire, { ...zeroUsage(), model_calls: 1, budget_refused: 2, cost_usd: 0.5 })).toEqual({
      ...wire,
      model_calls: 4,
      budget_refused: 2,
      cost_usd: 0.51234,
    });
  });

  it("sum to zero for no evaluations", () => {
    expect(sumUsage([])).toEqual(zeroUsage());
    expect(sumUsage([wire, wire]).judge_calls).toBe(4);
  });
});

describe("formatUsage / usageSummaryLine", () => {
  it("formats like the engine's report.FormatUsage", () => {
    expect(formatUsage(wire)).toBe("3 model calls · 1500 tokens (400 cached) · $0.0123 · retry cost $0.0005");
    expect(formatUsage({ ...wire, budget_refused: 2, unpriced_calls: 1 })).toBe(
      "3 model calls · 1500 tokens (400 cached) · $0.0123 · retry cost $0.0005 · 2 calls refused by budget · 1 unpriced calls not in $",
    );
  });

  it("prints only when a model call was made or refused", () => {
    expect(usageSummaryLine(zeroUsage())).toBeUndefined();
    expect(usageSummaryLine(wire)).toBe(`attest usage: ${formatUsage(wire)}`);
    expect(usageSummaryLine({ ...zeroUsage(), budget_refused: 1 })).toBe(
      "attest usage: 0 model calls · 0 tokens (0 cached) · $0.0000 · retry cost $0.0000 · 1 calls refused by budget",
    );
  });
});

describe("AttestClient.evaluateBatch usage and budget", () => {
  const trace = { trace_id: "trc_u", output: { message: "ok" }, steps: [] };

  function client(result: Record<string, unknown>) {
    const c = new AttestClient({} as EngineManager);
    const send = vi.spyOn(c, "sendRequest").mockResolvedValue(result as never);
    return { c, send };
  }

  it("parses usage and sends the batch budget only when given", async () => {
    const { c, send } = client({ results: [], usage: wire });
    const result = await c.evaluateBatch(trace, []);
    expect(result.usage).toEqual(wire);
    expect(send.mock.calls[0]![1]).not.toHaveProperty("budget");

    await c.evaluateBatch(trace, [], { budget: { max_judge_calls: 2, max_cost_usd: 0.5 } });
    expect(send.mock.calls[1]![1]).toMatchObject({ budget: { max_judge_calls: 2, max_cost_usd: 0.5 } });
  });

  it("rejects a result without usage", async () => {
    const { c } = client({ results: [] });
    await expect(c.evaluateBatch(trace, [])).rejects.toThrow("usage must be an object");
  });
});
