import * as vitest from "vitest";
import { setChainObserver, type ExpectChain } from "@attest-ai/core";
import { bindTestContext, runningTest, testScope } from "./test-state.js";

/** The error a test fails with when it built `count` chains and evaluated none of them. */
export function unevaluatedChainsMessage(count: number): string {
  return `${count} expect() chain${count === 1 ? "" : "s"} built but never evaluated`;
}

/**
 * Fail a test that builds an `expect()` chain it never passes to `evaluate()` or `inspect()`.
 * A per-test context owns the chains and finish handler, including concurrent test bodies.
 * Chains built outside a test are not tracked.
 */
export function installChainGuard(): void {
  const pending = new WeakMap<object, {
    retry: number;
    repeat: number;
    chains: Set<ExpectChain>;
  }>();
  const aroundEach = (vitest as Partial<typeof vitest>).aroundEach;
  if (aroundEach !== undefined) {
    aroundEach((run, context) =>
      testScope.run({ context, onTestFinished: context.onTestFinished }, run),
    );
  }
  vitest.beforeEach((context) => {
    if (aroundEach === undefined) bindTestContext(context);
  });
  setChainObserver((chain) => {
    // Outside the bound body, only serial hooks have an unambiguous global current test.
    const scoped = testScope.getStore();
    const test = scoped?.context.task ?? runningTest();
    if (test === undefined) return;
    if (scoped === undefined && test.concurrent === true) {
      throw new Error(
        "Attest cannot bind this concurrent test's chain guard. Build the chain inside the " +
          "test body, or upgrade to Vitest >=4.1. Enable test isolation if the integration is cached.",
      );
    }
    const retry = test.result?.retryCount ?? 0;
    const repeat = test.result?.repeatCount ?? 0;
    const state = pending.get(test);
    if (state !== undefined && state.retry === retry && state.repeat === repeat) {
      state.chains.add(chain);
      return;
    }
    const created = new Set([chain]);
    pending.set(test, { retry, repeat, chains: created });
    const onTestFinished = scoped?.onTestFinished ?? vitest.onTestFinished;
    onTestFinished.call(scoped?.context, () => {
      pending.delete(test);
      let unevaluated = 0;
      for (const entry of created) if (!entry.evaluated) unevaluated++;
      if (unevaluated > 0) throw new Error(unevaluatedChainsMessage(unevaluated));
    });
  });
}
