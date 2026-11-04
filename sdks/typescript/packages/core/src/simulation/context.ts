import { AsyncLocalStorage } from "node:async_hooks";

/** Mock tool implementations active for the current async scope. */
export type MockToolFn = (args: Record<string, unknown>) => unknown;

export const activeMockRegistry = new AsyncLocalStorage<ReadonlyMap<string, MockToolFn>>();

/**
 * Seeded PRNG (mulberry32): the same seed yields the same sequence. An
 * undefined seed uses `Math.random`. Sequences differ from Python's
 * `random.Random`; determinism holds within TS only.
 */
export function createRng(seed?: number): () => number {
  if (seed === undefined) return Math.random;
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
