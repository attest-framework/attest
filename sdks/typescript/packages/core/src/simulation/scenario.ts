import type { SimulatePersona } from "../proto/types.js";
import { OUTCOME_PASS, type Outcome } from "../proto/constants.js";
import type { AgentResult } from "../result.js";
import { activeMockRegistry, createRng, sleep, type MockToolFn } from "./context.js";
import {
  aggregateOutcomes,
  formatSkipReason,
  sumNotEvaluatedReasons,
  verdictsDiagnostics,
  type Verdict,
} from "./verdict.js";

export interface ScenarioConfig {
  persona?: SimulatePersona;
  mockTools: Readonly<Record<string, MockToolFn>>;
  faultErrorRate: number;
  faultLatencyJitterMs: number;
  maxTurns: number;
  seed?: number;
}

/** Passed to the scenario body on every turn. */
export interface ScenarioContext {
  readonly persona: SimulatePersona | undefined;
  /** Zero-based turn index. */
  readonly turn: number;
}

/** One verdict per turn; the aggregate follows the assertion-status rules. */
export class ScenarioResult implements Verdict {
  readonly config: ScenarioConfig;
  readonly results: readonly AgentResult[];

  constructor(config: ScenarioConfig, results: readonly AgentResult[] = []) {
    this.config = config;
    this.results = results;
  }

  /** True when the aggregate `outcome()` is pass. */
  get passed(): boolean {
    return this.outcome() === OUTCOME_PASS;
  }

  outcome(options?: { strict?: boolean }): Outcome {
    return aggregateOutcomes(this.results.map((r) => r.outcome(options)));
  }

  get notEvaluatedReasons(): Readonly<Record<string, number>> {
    return sumNotEvaluatedReasons(this.results);
  }

  skipReason(): string {
    return formatSkipReason(this.notEvaluatedReasons);
  }

  renderDiagnostics(options?: { color?: boolean }): string {
    return verdictsDiagnostics(this.results, "turn", options?.color);
  }
}

export interface ScenarioOptions {
  persona?: SimulatePersona;
  mockTools?: Record<string, MockToolFn>;
  /** Probability per turn of throwing `Injected fault at turn N`. Default 0. */
  faultErrorRate?: number;
  /** Upper bound of uniform random delay per turn, in ms. Default 0. */
  faultLatencyJitterMs?: number;
  /** Turns to run. Default 1. */
  maxTurns?: number;
  /** Seed for a deterministic fault sequence. */
  seed?: number;
}

/**
 * Run `fn` for `maxTurns` turns with mock tools active, injecting faults
 * and latency per turn. `fn` receives the persona and turn index.
 */
export function scenario<A extends unknown[]>(
  options: ScenarioOptions,
  fn: (ctx: ScenarioContext, ...args: A) => AgentResult | Promise<AgentResult>,
): (...args: A) => Promise<ScenarioResult> {
  const config: ScenarioConfig = {
    persona: options.persona,
    mockTools: { ...options.mockTools },
    faultErrorRate: options.faultErrorRate ?? 0,
    faultLatencyJitterMs: options.faultLatencyJitterMs ?? 0,
    maxTurns: options.maxTurns ?? 1,
    seed: options.seed,
  };

  return async (...args: A): Promise<ScenarioResult> => {
    const run = async (): Promise<ScenarioResult> => {
      const results: AgentResult[] = [];
      const rng = createRng(config.seed);
      for (let turn = 0; turn < config.maxTurns; turn++) {
        if (config.faultErrorRate > 0 && rng() < config.faultErrorRate) {
          throw new Error(`Injected fault at turn ${turn}`);
        }
        if (config.faultLatencyJitterMs > 0) {
          await sleep(rng() * config.faultLatencyJitterMs);
        }
        results.push(await fn({ persona: config.persona, turn }, ...args));
      }
      return new ScenarioResult(config, results);
    };

    const mocks = new Map(Object.entries(config.mockTools));
    return mocks.size > 0 ? activeMockRegistry.run(mocks, run) : run();
  };
}
