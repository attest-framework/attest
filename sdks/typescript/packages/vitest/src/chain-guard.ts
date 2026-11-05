import { onTestFinished } from "vitest";
import { setChainObserver, type ExpectChain } from "@attest-ai/core";
import { runningTest } from "./test-state.js";

/** The error a test fails with when it built `count` chains and evaluated none of them. */
export function unevaluatedChainsMessage(count: number): string {
  return `${count} expect() chain${count === 1 ? "" : "s"} built but never evaluated`;
}

/**
 * Fail a test that builds an `expect()` chain it never passes to `evaluate()` or `inspect()`.
 * Each chain created while a test runs registers a finish handler on that test; the handler
 * throws, which fails the test, when any chain of the test is still unevaluated. Chains built
 * outside a test, and in a `test.concurrent` test (the running test is ambiguous there), are
 * not tracked.
 */
export function installChainGuard(): void {
  const pending = new WeakMap<object, ExpectChain[]>();
  setChainObserver((chain) => {
    void runningTest().then((test) => {
      if (test === undefined || test.concurrent === true) return;
      const chains = pending.get(test);
      if (chains !== undefined) {
        chains.push(chain);
        return;
      }
      const created = [chain];
      pending.set(test, created);
      onTestFinished(() => {
        const unevaluated = created.filter((c) => !c.evaluated).length;
        if (unevaluated > 0) throw new Error(unevaluatedChainsMessage(unevaluated));
      });
    });
  });
}
