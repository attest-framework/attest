import { createRng, sleep } from "./context.js";

export interface FaultInjectOptions {
  /** Probability in [0, 1] that a call throws. Default 0. */
  errorRate?: number;
  /** Upper bound of uniform random delay per call, in ms. Default 0. */
  latencyJitterMs?: number;
  /** Seed for a deterministic fault sequence. */
  seed?: number;
}

/**
 * Wrap `fn` so calls throw `Error("Injected fault in <name>")` at
 * `errorRate` and are delayed by up to `latencyJitterMs`. The RNG is
 * created once per wrapped function, so a fixed seed gives a fixed
 * sequence across calls.
 */
export function faultInject<A extends unknown[], R>(
  options: FaultInjectOptions,
  fn: (...args: A) => R | Promise<R>,
): (...args: A) => Promise<R> {
  const errorRate = options.errorRate ?? 0;
  const latencyJitterMs = options.latencyJitterMs ?? 0;
  const rng = createRng(options.seed);
  return async (...args: A): Promise<R> => {
    if (errorRate > 0 && rng() < errorRate) {
      throw new Error(`Injected fault in ${fn.name || "anonymous"}`);
    }
    if (latencyJitterMs > 0) {
      await sleep(rng() * latencyJitterMs);
    }
    return fn(...args);
  };
}
