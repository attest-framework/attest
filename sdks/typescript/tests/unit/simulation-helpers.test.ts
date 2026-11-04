import { describe, it, expect, vi } from "vitest";
import {
  AgentResult,
  FRIENDLY_USER,
  MockToolRegistry,
  RepeatResult,
  ScenarioResult,
  TraceBuilder,
  faultInject,
  mockTool,
  repeat,
  scenario,
} from "../../packages/core/src/index.js";
import type { AssertionResult, Trace } from "../../packages/core/src/index.js";

function verdict(status: string): AgentResult {
  const row: AssertionResult = {
    assertion_id: "a",
    status,
    score: 0,
    explanation: "",
    ...(status === "not_evaluated" ? { reason: "simulation" } : {}),
  } as AssertionResult;
  const trace = { trace_id: "t", output: {} } as unknown as Trace;
  return new AgentResult(trace, [row]);
}

function searchTrace(): Trace {
  return new TraceBuilder("test")
    .addToolCall("search", { args: { query: "hello" } })
    .setOutput({ result: "done" })
    .build();
}

describe("mockTool", () => {
  it("tags the function with its tool name and keeps its behavior", () => {
    const fake = mockTool("lookup", (args) => ({ value: args["key"] }));
    expect(fake.mockToolName).toBe("lookup");
    expect(fake({ key: "abc" })).toEqual({ value: "abc" });
  });
});

describe("MockToolRegistry", () => {
  it("intercepts a registered tool inside run and not outside", () => {
    const registry = new MockToolRegistry();
    registry.register("search", (args) => ({ mocked: args["query"] }));

    const inside = registry.run(() => searchTrace());
    expect(inside.steps[0]?.result).toEqual({ mocked: "hello" });

    const outside = searchTrace();
    expect(outside.steps[0]?.result).toBeUndefined();
  });

  it("keeps the original result for tools without a mock", () => {
    const registry = new MockToolRegistry();
    registry.register("other", () => ({ mocked: true }));
    const trace = registry.run(() =>
      new TraceBuilder("t")
        .addToolCall("search", { args: {}, result: { original: 1 } })
        .setOutput({})
        .build(),
    );
    expect(trace.steps[0]?.result).toEqual({ original: 1 });
  });

  it("rejects an async mock instead of recording a Promise", () => {
    const registry = new MockToolRegistry();
    registry.register("search", (async () => ({ ok: true })) as () => unknown);

    expect(() => registry.run(() => searchTrace())).toThrow(
      "mock for tool 'search' returned a Promise",
    );
  });

  it("isolates concurrent scopes", async () => {
    const a = new MockToolRegistry();
    a.register("search", () => ({ from: "a" }));
    const b = new MockToolRegistry();
    b.register("search", () => ({ from: "b" }));
    const wait = (): Promise<void> => new Promise((r) => setTimeout(r, 5));

    const [ra, rb] = await Promise.all([
      a.run(async () => {
        await wait();
        return searchTrace();
      }),
      b.run(async () => {
        await wait();
        return searchTrace();
      }),
    ]);
    expect(ra.steps[0]?.result).toEqual({ from: "a" });
    expect(rb.steps[0]?.result).toEqual({ from: "b" });
  });
});

describe("faultInject", () => {
  it("never throws at error rate 0", async () => {
    const fn = faultInject({ errorRate: 0 }, () => "ok");
    for (let i = 0; i < 20; i++) expect(await fn()).toBe("ok");
  });

  it("always throws at error rate 1, naming the function", async () => {
    const fn = faultInject({ errorRate: 1 }, function myFn() {
      return "ok";
    });
    await expect(fn()).rejects.toThrow("Injected fault in myFn");
  });

  it("is deterministic for a fixed seed and mixes outcomes at 0.5", async () => {
    const run = async (): Promise<boolean[]> => {
      const fn = faultInject({ errorRate: 0.5, seed: 99 }, () => "ok");
      const out: boolean[] = [];
      for (let i = 0; i < 20; i++) {
        out.push(await fn().then(() => true, () => false));
      }
      return out;
    };
    const first = await run();
    expect(await run()).toEqual(first);
    expect(first).toContain(true);
    expect(first).toContain(false);
  });

  it("delays each call by the seeded jitter before running", async () => {
    vi.useFakeTimers();
    try {
      let ran = false;
      const fn = faultInject({ latencyJitterMs: 1000, seed: 3 }, () => {
        ran = true;
        return "ok";
      });
      const pending = fn();
      await vi.advanceTimersByTimeAsync(0);
      expect(ran).toBe(false);
      await vi.advanceTimersByTimeAsync(1000);
      expect(await pending).toBe("ok");
      expect(ran).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("scenario", () => {
  it("passes persona and turn, and activates mock tools", async () => {
    const seen: unknown[] = [];
    const mocked: unknown[] = [];
    const run = scenario(
      { persona: FRIENDLY_USER, mockTools: { search: (a) => ({ mocked: a["query"] }) } },
      (ctx) => {
        seen.push(ctx);
        mocked.push(searchTrace().steps[0]?.result);
        return verdict("pass");
      },
    );
    const result = await run();
    expect(result).toBeInstanceOf(ScenarioResult);
    expect(result.results).toHaveLength(1);
    expect(seen).toEqual([{ persona: FRIENDLY_USER, turn: 0 }]);
    expect(mocked).toEqual([{ mocked: "hello" }]);
    expect(result.passed).toBe(true);
  });

  it("runs maxTurns turns", async () => {
    const turns: number[] = [];
    const result = await scenario({ maxTurns: 3 }, (ctx) => {
      turns.push(ctx.turn);
      return verdict("pass");
    })();
    expect(turns).toEqual([0, 1, 2]);
    expect(result.results).toHaveLength(3);
  });

  it("activates mocks during the body and clears them after", async () => {
    const run = scenario({ mockTools: { search: () => ({ mocked: true }) } }, () => {
      const trace = searchTrace();
      expect(trace.steps[0]?.result).toEqual({ mocked: true });
      return verdict("pass");
    });
    await run();
    expect(searchTrace().steps[0]?.result).toBeUndefined();
  });

  it("throws the injected fault at error rate 1", async () => {
    const run = scenario({ faultErrorRate: 1, seed: 1 }, () => verdict("pass"));
    await expect(run()).rejects.toThrow("Injected fault at turn 0");
  });

  it("injects the same fault turn for a fixed seed and a different one for another seed", async () => {
    const faultTurn = (seed: number): Promise<string> =>
      scenario({ faultErrorRate: 0.3, maxTurns: 50, seed }, () => verdict("pass"))().then(
        () => "none",
        (e: Error) => e.message,
      );
    const [a1, a2, b] = [await faultTurn(7), await faultTurn(7), await faultTurn(8)];
    expect(a1).toMatch(/^Injected fault at turn \d+$/);
    expect(a2).toBe(a1);
    const others = await Promise.all([1, 2, 3, 4, 5].map(faultTurn));
    expect(new Set([a1, b, ...others]).size).toBeGreaterThan(1);
  });

  it("aggregates turn outcomes: soft_fail passes, hard_fail fails with diagnostics", () => {
    const cfg = { mockTools: {}, faultErrorRate: 0, faultLatencyJitterMs: 0, maxTurns: 2 };
    const soft = new ScenarioResult(cfg, [verdict("pass"), verdict("soft_fail")]);
    expect(soft.outcome()).toBe("pass");
    expect(soft.passed).toBe(true);

    const hard = new ScenarioResult(cfg, [verdict("pass"), verdict("hard_fail")]);
    expect(hard.outcome()).toBe("fail");
    expect(hard.passed).toBe(false);
    expect(hard.renderDiagnostics()).toContain("turn 2/2");
  });
});

describe("repeat", () => {
  it("runs the function n times", async () => {
    let calls = 0;
    const result = await repeat(4, () => {
      calls++;
      return verdict("pass");
    })();
    expect(calls).toBe(4);
    expect(result.count).toBe(4);
    expect(result.passRate).toBe(1);
  });

  it("reports a null pass rate for no trials", () => {
    expect(new RepeatResult([]).passRate).toBeNull();
  });

  it("excludes not_evaluated trials from the pass rate", () => {
    const result = new RepeatResult([verdict("pass"), verdict("soft_fail"), verdict("not_evaluated")]);
    expect(result.notEvaluated).toBe(1);
    expect(result.passRate).toBe(1);
    expect(result.outcome()).toBe("skip");
    expect(result.outcome({ strict: true })).toBe("fail");
    expect(result.skipReason()).toBe("not evaluated: simulation");
  });

  it("fails on any failing trial", () => {
    const result = new RepeatResult([verdict("pass"), verdict("hard_fail")]);
    expect(result.outcome()).toBe("fail");
    expect(result.passRate).toBe(0.5);
    expect(result.allPassed).toBe(false);
    expect(result.renderDiagnostics()).toContain("trial 2/2");
  });

  it("nests scenario results as trials", async () => {
    const result = await repeat(2, scenario({}, () => verdict("pass")))();
    expect(result.outcome()).toBe("pass");
  });
});
