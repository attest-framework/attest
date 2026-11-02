import * as vitest from "vitest";
import type { TestContext } from "vitest";
import {
  type ExpectChain,
  AgentResult,
  AttestClient,
  EngineManager,
  isSimulationMode,
  renderDiagnostics,
  requireSimulationCapability,
  OUTCOME_FAIL,
  OUTCOME_SKIP,
  STATUS_SOFT_FAIL,
} from "@attest-ai/core";

export interface InspectOptions {
  /** Throw when the cumulative session cost exceeds this USD amount. */
  budget?: number;
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

type RunningTest = NonNullable<TestContext["task"]>;

/**
 * The running test: `TestRunner.getCurrentTest()` on Vitest 4.1+, `vitest/suite`
 * before that (loaded only there, because Vitest 4.1 warns when it is imported).
 */
async function runningTest(): Promise<RunningTest | undefined> {
  const runner = (vitest as Partial<typeof vitest>).TestRunner;
  if (runner !== undefined) return runner.getCurrentTest();
  const suite = await import("vitest/suite");
  return suite.getCurrentTest();
}

/**
 * Attach the results to the running test's meta so AttestReporter, in the main
 * process, can write them with the test's file and line. Results from an earlier
 * attempt of a retried test are dropped, so the report matches the outcome Vitest
 * reports. Outside a test (e.g. in a beforeAll hook) there is no test to attach to.
 */
async function recordOnTest(result: AgentResult, context?: TestContext): Promise<void> {
  const test = context?.task ?? (await runningTest());
  if (test === undefined) return;
  if (context === undefined && test.concurrent === true) {
    throw new Error(
      "Attest results from a test.concurrent test — pass the test context: " +
        "inspect(chain, { context }) or evaluate(chain, { context })",
    );
  }
  const attempt = test.result?.retryCount ?? 0;
  let meta = test.meta.attest;
  if (meta === undefined || meta.attempt !== attempt) {
    meta = { attempt, results: [], totalCost: 0, totalDurationMs: 0 };
    test.meta.attest = meta;
  }
  meta.results.push(...result.assertionResults);
  meta.totalCost += result.totalCost;
  meta.totalDurationMs += result.totalDurationMs;
}

function strictFromEnv(): boolean {
  const v = (process.env["ATTEST_STRICT"] ?? "").trim().toLowerCase();
  return v === "1" || v === "true" || v === "yes";
}

/**
 * Fail or skip the running Vitest test according to the batch outcome:
 * any hard_fail throws with rendered diagnostics; otherwise any
 * not_evaluated skips with "not evaluated: <reasons>" (throws when strict);
 * soft failures pass.
 */
export function enforceOutcome(
  result: AgentResult,
  strict: boolean,
  context?: TestContext,
): AgentResult {
  const outcome = result.outcome({ strict });
  if (outcome === OUTCOME_FAIL) {
    const header = result.hardFailures.length === 0 ? `strict: ${result.skipReason()}\n` : "";
    throw new Error(header + renderDiagnostics(result).replace(/^\n/, ""));
  }
  if (outcome === OUTCOME_SKIP) {
    (context ?? currentTestContext(result)).skip(result.skipReason());
  }
  return result;
}

/**
 * The running test's context when Vitest can name it unambiguously:
 * TestRunner exists from Vitest 4.1, and under test.concurrent the
 * "current test" may be another test, so the caller must pass `context`.
 */
function currentTestContext(result: AgentResult): TestContext {
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
    this._client = new AttestClient(this.manager);
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
    await this.ensureStarted();
    const result = await this.client.evaluateBatch(chain.trace, chain.assertions);

    globalThis.__attest_session_cost__ =
      (globalThis.__attest_session_cost__ ?? 0) + (result.total_cost ?? 0);

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
    );
    await recordOnTest(agentResult, options?.context);

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

/** Evaluate and enforce: hard_fail throws, not_evaluated skips (strict: throws). */
export async function evaluate(
  chain: ExpectChain,
  options?: EvaluateOptions,
): Promise<AgentResult> {
  return useAttest().evaluate(chain, options);
}

/** Evaluate without enforcing; use for trial loops and custom checks. */
export async function inspect(
  chain: ExpectChain,
  options?: InspectOptions,
): Promise<AgentResult> {
  return useAttest().inspect(chain, options);
}
