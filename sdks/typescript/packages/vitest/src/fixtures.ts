import * as vitest from "vitest";
import type { TestContext } from "vitest";
import {
  type ExpectChain,
  AgentResult,
  STATUS_SOFT_FAIL,
  AttestClient,
  EngineManager,
  isSimulationMode,
  renderVerdictDiagnostics,
  type Verdict,
  requireSimulationCapability,
  OUTCOME_FAIL,
  OUTCOME_SKIP,
  zeroUsage,
  type BatchBudget,
  PluginRegistry,
  submitPluginResults,
} from "@attest-ai/core";
import {
  assignAssertionIds,
  batchContextOf,
  attemptState,
  inTrial,
  recordAgentSpend,
  recordBudgetExceeded,
  recordOnTest,
  recordUnattachedUsage,
  remainingBudget,
  resolveTest,
  strictFromEnv,
  trialScope,
  validateBudget,
  type TestBudget,
} from "./test-state.js";

/**
 * Plugins that back `.plugin(id)` assertions. Register under the plugin's own name with
 * `pluginType` "assertion": `attestPlugins.register(plugin.name, plugin)`. The SDK runs the
 * plugin and submits its result to the engine before the batch that holds the assertion.
 */
export const attestPlugins = new PluginRegistry();

export interface InspectOptions {
  /** Throw when the cumulative session cost exceeds this USD amount. */
  budget?: number;
  /**
   * Limits for this test's attest model calls. Each evaluation sends what is
   * left: judge calls not yet used in the test, and `maxCostUsd` minus the
   * test's attest cost and agent spend so far (trace `metadata.cost_usd`). A
   * call that does not fit is not sent and its assertion is not_evaluated with
   * reason "budget_exhausted". Inside trials(), defaults to the trials budget.
   */
  testBudget?: TestBudget;
  /**
   * The running test's context (`it("…", async (context) => …)`). Used to
   * attach results to the right test for AttestReporter and to skip it;
   * required under `test.concurrent` and, for skips, on Vitest before 4.1.
   */
  context?: TestContext;
}

export interface EvaluateOptions extends InspectOptions {
  /**
   * Fail (instead of skip) when an assertion was not evaluated. Defaults to
   * the ATTEST_STRICT environment variable ("1", "true" or "yes").
   */
  strict?: boolean;
}

/**
 * Fail or skip the running Vitest test according to the batch outcome:
 * any hard_fail throws with rendered diagnostics; otherwise any
 * not_evaluated skips with "not evaluated: <reasons>" (throws when strict);
 * soft failures pass. Inside a trial of trials() (N > 1) it does nothing and
 * returns `result`: trials() enforces the aggregate outcome after the last trial.
 */
export function enforceOutcome<V extends Verdict>(
  result: V,
  strict: boolean,
  context?: TestContext,
): V {
  if (inTrial()) return result;
  const outcome = result.outcome({ strict });
  if (outcome === OUTCOME_FAIL) {
    const header = result.outcome({ strict: false }) === OUTCOME_FAIL ? "" : `strict: ${result.skipReason()}\n`;
    throw new Error(header + renderVerdictDiagnostics(result).replace(/^\n/, ""));
  }
  if (outcome === OUTCOME_SKIP) {
    (context ?? trialScope.getStore()?.test.context ?? currentTestContext(result)).skip(
      result.skipReason(),
    );
  }
  return result;
}

/**
 * The running test's context when Vitest can name it unambiguously:
 * TestRunner exists from Vitest 4.1, and under test.concurrent the
 * "current test" may be another test, so the caller must pass `context`.
 */
function currentTestContext(result: Verdict): TestContext {
  const runner = (vitest as Partial<typeof vitest>).TestRunner;
  const test = runner?.getCurrentTest();
  if (test === undefined || test.concurrent === true) {
    throw new Error(
      `${result.skipReason()} — pass the test context to skip this test: ` +
        "evaluate(chain, { context }) (required for test.concurrent and Vitest < 4.1)",
    );
  }
  return test.context;
}

export class AttestEngineFixture {
  private manager: EngineManager | undefined;
  private _client: AttestClient | undefined;
  private starting: Promise<void> | undefined;

  constructor(options?: { enginePath?: string; logLevel?: string; client?: AttestClient }) {
    if (options?.client !== undefined) {
      this._client = options.client;
      return;
    }
    this.manager = new EngineManager({
      enginePath: options?.enginePath,
      logLevel: options?.logLevel ?? "warn",
    });
  }

  /**
   * Start the engine. In simulation mode the engine is started with
   * ATTEST_SIMULATION=1 so it evaluates layers 1-4 and reports layers 5-6
   * as not evaluated; an engine without that capability is rejected.
   */
  async start(): Promise<void> {
    if (this.manager === undefined) {
      throw new Error("AttestEngineFixture already stopped.");
    }
    const simulation = isSimulationMode();
    if (simulation) {
      process.env["ATTEST_SIMULATION"] = "1";
    }
    const init = await this.manager.start();
    if (simulation) {
      try {
        requireSimulationCapability(init);
      } catch (err) {
        await this.manager.stop();
        throw err;
      }
    }
    const routed = this.manager.client;
    if (routed === undefined) throw new Error("Engine started without its id-routed client.");
    this._client = routed;
  }

  async stop(): Promise<void> {
    if (this.manager) {
      await this.manager.stop();
      this.manager = undefined;
      this._client = undefined;
    }
  }

  get client(): AttestClient {
    if (this._client === undefined) {
      throw new Error("Engine not started. Call start() first.");
    }
    return this._client;
  }

  private async ensureStarted(): Promise<void> {
    if (this._client !== undefined) return;
    this.starting ??= this.start();
    await this.starting;
  }

  /** Evaluate collected assertions without enforcing the test outcome. */
  async inspect(chain: ExpectChain, options?: InspectOptions): Promise<AgentResult> {
    chain.markEvaluated();
    if (chain.assertions.length === 0) {
      throw new Error("expect() chain has no assertions: add at least one assertion before evaluating it");
    }
    await this.ensureStarted();
    // Inside a test, assertion IDs are scheme 2 (C3): a hash of the test's report test_id, the
    // assertion type and spec, and its occurrence among identical assertions of the test
    // (restarting each trial), so trials and baselines match rows by ID. Outside a test they
    // stay random.
    const test = await resolveTest(options?.context);
    const scope = trialScope.getStore();
    const trial = scope !== undefined && scope.test === test ? scope.trial : undefined;
    const budget =
      options?.testBudget !== undefined
        ? validateBudget(options.testBudget)
        : scope !== undefined && scope.test === test
          ? scope.budget
          : undefined;
    let assertions = chain.assertions;
    let batchBudget: BatchBudget | undefined;
    if (test !== undefined) {
      assertions = assignAssertionIds(test, assertions);
      recordAgentSpend(test, chain.trace);
      if (budget !== undefined) {
        const state = attemptState(test);
        batchBudget = remainingBudget(budget, state.usage, state.agentSpendUsd);
      }
    } else if (budget !== undefined) {
      // Outside a test the budget covers this one evaluation.
      batchBudget = remainingBudget(budget, zeroUsage(), chain.trace.metadata?.cost_usd ?? 0);
    }
    await submitPluginResults(attestPlugins, this.client, chain.trace, assertions);
    const result = await this.client.evaluateBatch(chain.trace, assertions, {
      ...(batchBudget === undefined ? {} : { budget: batchBudget }),
      ...(test === undefined ? {} : { context: batchContextOf(test) }),
    });

    globalThis.__attest_session_cost__ =
      (globalThis.__attest_session_cost__ ?? 0) + (result.total_cost ?? 0);

    if (result.budget_exceeded === true) recordBudgetExceeded(test);

    const softFailCount = (result.results ?? []).filter(
      (r) => r.status === STATUS_SOFT_FAIL,
    ).length;
    globalThis.__attest_session_soft_failures__ =
      (globalThis.__attest_session_soft_failures__ ?? 0) + softFailCount;

    const agentResult = new AgentResult(
      chain.trace,
      result.results ?? [],
      result.total_cost ?? 0,
      result.total_duration_ms ?? 0,
      result.usage,
    );
    if (test !== undefined) recordOnTest(test, agentResult, trial);
    else recordUnattachedUsage(agentResult.usage);
    if (scope !== undefined && scope.test === test) scope.results.push(agentResult);

    // Track non-passing AgentResults so AttestCostReporter can render the
    // diagnostic block at the end of the run. Bound size because long
    // suites with many failures should not balloon RAM.
    if (!agentResult.passed) {
      const list = (globalThis.__attest_session_failures__ ??= []);
      if (list.length < 200) {
        list.push(agentResult);
      }
    }

    if (
      options?.budget !== undefined &&
      globalThis.__attest_session_cost__ > options.budget
    ) {
      throw new Error(
        `Attest budget exceeded: cost $${globalThis.__attest_session_cost__.toFixed(6)}` +
          ` > budget $${options.budget.toFixed(6)}`,
      );
    }

    return agentResult;
  }

  /** Evaluate collected assertions and enforce the test outcome. */
  async evaluate(chain: ExpectChain, options?: EvaluateOptions): Promise<AgentResult> {
    const result = await this.inspect(chain, options);
    return enforceOutcome(result, options?.strict ?? strictFromEnv(), options?.context);
  }

  /**
   * Fail or skip the running test according to a verdict's outcome: an
   * `AgentResult` from `inspect()` or the aggregate of `scenario()` /
   * `repeat()`. See `enforceOutcome`.
   */
  enforce<V extends Verdict>(verdict: V, options?: { strict?: boolean; context?: TestContext }): V {
    return enforceOutcome(verdict, options?.strict ?? strictFromEnv(), options?.context);
  }
}

// Vitest runs globalSetup in the main process and tests in workers, so a
// worker that cannot see the global client starts its own engine on first use.
// The engine exits when its stdin closes, i.e. when the worker exits.
let workerFixture: AttestEngineFixture | undefined;

export function useAttest(): AttestEngineFixture {
  const client = globalThis.__attest_client__;
  if (client !== undefined) {
    return new AttestEngineFixture({ client });
  }
  workerFixture ??= new AttestEngineFixture();
  return workerFixture;
}

/**
 * Evaluate and enforce: hard_fail throws, not_evaluated skips (strict: throws).
 * Inside a trial of trials() (N > 1) it does not enforce, like inspect().
 */
export async function evaluate(
  chain: ExpectChain,
  options?: EvaluateOptions,
): Promise<AgentResult> {
  return useAttest().evaluate(chain, options);
}

/** Evaluate without enforcing; use for custom checks. For repeated runs use trials(). */
export async function inspect(
  chain: ExpectChain,
  options?: InspectOptions,
): Promise<AgentResult> {
  return useAttest().inspect(chain, options);
}
