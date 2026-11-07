/**
 * Per-test state shared by evaluate/inspect and trials: deterministic assertion
 * IDs, the per-test budget remainder, and the results recorded on task meta.
 */
import { AsyncLocalStorage } from "node:async_hooks";
import { randomUUID } from "node:crypto";
import { mkdirSync, writeFileSync } from "node:fs";
import { join, relative, sep } from "node:path";
import * as vitest from "vitest";
import type { TestContext } from "vitest";
import {
  AssertionIdAssigner,
  addUsage,
  zeroUsage,
  type AgentResult,
  type Assertion,
  type BatchBudget,
  type BatchContext,
  type Trace,
  type Usage,
} from "@attest-ai/core";
import { reportTracesEnabled, TraceTable, type AttestTaskMeta } from "./report.js";
import { BUDGET_MARKER_EXT, ROOT_ENV, USAGE_DIR_ENV } from "./process-env.js";

export type RunningTest = NonNullable<TestContext["task"]>;

/** Limits for the attest model calls of one test (all its evaluations and trials). */
export interface TestBudget {
  /** Judge-provider requests the test may make. */
  maxJudgeCalls?: number;
  /** USD the test may spend: attest judge/embedding cost plus agent spend (trace `metadata.cost_usd`). */
  maxCostUsd?: number;
}

/** The trials() call a body runs in; `trial` is undefined for a single run (N = 1). */
export interface TrialScope {
  test: RunningTest;
  trial: number | undefined;
  budget: TestBudget | undefined;
  /** Evaluations of the current trial. */
  results: AgentResult[];
}

export const trialScope = new AsyncLocalStorage<TrialScope>();

/** True while the code runs inside trial i of a trials() call with N > 1. */
export function inTrial(): boolean {
  return trialScope.getStore()?.trial !== undefined;
}

interface AttemptState {
  attempt: number;
  /** Gives ids to the assertions of this test (this trial, under trials()). */
  ids: AssertionIdAssigner;
  /** Attest usage of this attempt so far. */
  usage: Usage;
  /** Sum of `metadata.cost_usd` of the traces evaluated in this attempt. */
  agentSpendUsd: number;
  traceIds: Set<string>;
}

const attempts = new WeakMap<RunningTest, AttemptState>();

function attemptOf(test: RunningTest): number {
  return test.result?.retryCount ?? 0;
}

/** State of the running attempt of `test`; a retried test starts afresh. */
export function attemptState(test: RunningTest): AttemptState {
  const attempt = attemptOf(test);
  let state = attempts.get(test);
  if (state === undefined || state.attempt !== attempt) {
    state = { attempt, ids: new AssertionIdAssigner(testIdOf(test)), usage: zeroUsage(), agentSpendUsd: 0, traceIds: new Set() };
    attempts.set(test, state);
  }
  return state;
}

type TestBody = () => unknown;
interface TestFunctions {
  getFn(test: RunningTest): TestBody | undefined;
  setFn(test: RunningTest, fn: TestBody): void;
}

// Vitest before 4.1 has no TestRunner; its running test comes from `vitest/suite`. Loaded only
// there (4.1 warns when it is imported) and at module load, so the lookup stays synchronous:
// an async lookup resolves after a synchronous test body already finished. The specifier is a
// variable because Vite resolves a literal one while transforming this file, and Vitest 5 no
// longer exports `./suite`, which would fail the import even though this branch never runs.
const LEGACY_SUITE = "vitest/suite";
const legacySuite =
  (vitest as Partial<typeof vitest>).TestRunner === undefined
    ? ((await import(/* @vite-ignore */ LEGACY_SUITE)) as TestFunctions & {
        getCurrentTest(): RunningTest | undefined;
      })
    : undefined;

/** The running test: `TestRunner.getCurrentTest()` on Vitest 4.1+, `vitest/suite` before that. */
export function runningTest(): RunningTest | undefined {
  const runner = (vitest as Partial<typeof vitest>).TestRunner;
  return runner !== undefined ? runner.getCurrentTest() : legacySuite?.getCurrentTest();
}

/** The context of the test body, preserved across awaits and concurrent sibling tests. */
export const testScope = new AsyncLocalStorage<{
  context: TestContext;
  // Vitest temporarily replaces this method while finish/fail handlers run.
  onTestFinished: TestContext["onTestFinished"];
}>();
const boundTests = new WeakSet<RunningTest>();

/**
 * Before Vitest's aroundEach API, bind the runner's test callback. Merely entering an async
 * scope in beforeEach does not enclose the body. vitest/suite was removed in Vitest 5.
 */
export function bindTestContext(context: TestContext): void {
  const test = context.task;
  if (boundTests.has(test)) return;
  const fn = legacySuite?.getFn(test);
  if (fn === undefined) throw new Error("Attest cannot bind the Vitest test context");
  const scope = { context, onTestFinished: context.onTestFinished };
  legacySuite?.setFn(test, () => testScope.run(scope, fn));
  boundTests.add(test);
}

/**
 * The test an evaluation belongs to: the passed context, else the enclosing
 * trials() call, else Vitest's running test. Under test.concurrent the running
 * test may be another test, so outside trials() the caller must pass `context`.
 */
export async function resolveTest(context: TestContext | undefined): Promise<RunningTest | undefined> {
  if (context !== undefined) return context.task;
  const scope = trialScope.getStore();
  if (scope !== undefined) return scope.test;
  const test = runningTest();
  if (test !== undefined && test.concurrent === true) {
    throw new Error(
      "Attest results from a test.concurrent test — pass the test context: " +
        "inspect(chain, { context }), evaluate(chain, { context }) or trials(body, { context })",
    );
  }
  return test;
}

/**
 * The report's test_id of `test`: "<file> > <suites> > <name>", the same string
 * AttestReporter writes. The file is relative to the Vitest root AttestReporter
 * publishes (in a multi-project setup, not the project root); without
 * AttestReporter it is the file task's name, relative to the project root.
 */
export function testIdOf(test: RunningTest): string {
  const names = [test.name];
  for (let suite = test.suite; suite !== undefined && suite !== test.file; suite = suite.suite) {
    names.unshift(suite.name);
  }
  return `${testFileOf(test)} > ${names.join(" > ")}`;
}

function testFileOf(test: RunningTest): string {
  const root = process.env[ROOT_ENV];
  return root === undefined || test.file.filepath === undefined
    ? test.file.name
    : relative(root, test.file.filepath).split(sep).join("/");
}

/**
 * The `evaluate_batch` context naming `test`: its report test_id, its file and
 * the line it starts on. The line is present only when Vitest records task
 * locations (`test.includeTaskLocation`).
 */
export function batchContextOf(test: RunningTest): BatchContext {
  const line = test.location?.line;
  return {
    test_id: testIdOf(test),
    file: testFileOf(test),
    ...(line !== undefined ? { line } : {}),
  };
}

/**
 * Hand the usage of an evaluation that belongs to no test (beforeAll, a script)
 * to AttestReporter: one file per evaluation in its usage directory, so the
 * session usage counts it once. Without AttestReporter there is no report to add it to.
 */
export function recordUnattachedUsage(usage: Usage): void {
  const dir = process.env[USAGE_DIR_ENV];
  if (dir === undefined) return;
  if (Object.values(usage).every((v) => v === 0)) return;
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, `${randomUUID()}.json`), JSON.stringify(usage));
}

/** Restart the occurrence counts of `test`: each trial of trials() gives its assertions the same ids. */
export function restartAssertionIds(test: RunningTest): void {
  attemptState(test).ids = new AssertionIdAssigner(testIdOf(test));
}

/**
 * Give each assertion its scheme-2 id (contract C3): the hash of the test's report test_id, the
 * assertion type, its canonical spec and its occurrence index among identical assertions of the
 * test. The assertion also carries the test id as `scope_id`.
 */
export function assignAssertionIds(
  test: RunningTest,
  assertions: readonly Assertion[],
): Assertion[] {
  const { ids } = attemptState(test);
  return assertions.map((a) => ({ ...a, assertion_id: ids.next(a.type, a.spec), scope_id: ids.scopeId }));
}

/** Reject a budget with unknown keys or negative/non-numeric limits. */
export function validateBudget(budget: TestBudget): TestBudget {
  for (const key of Object.keys(budget)) {
    if (key !== "maxJudgeCalls" && key !== "maxCostUsd") {
      throw new Error(`Attest budget: unknown option "${key}" (expected maxJudgeCalls, maxCostUsd)`);
    }
  }
  const { maxJudgeCalls, maxCostUsd } = budget;
  if (maxJudgeCalls !== undefined && !(Number.isInteger(maxJudgeCalls) && maxJudgeCalls >= 0)) {
    throw new Error(`Attest budget: maxJudgeCalls must be an integer >= 0, got ${String(maxJudgeCalls)}`);
  }
  if (maxCostUsd !== undefined && !(Number.isFinite(maxCostUsd) && maxCostUsd >= 0)) {
    throw new Error(`Attest budget: maxCostUsd must be a number >= 0, got ${String(maxCostUsd)}`);
  }
  return budget;
}

/**
 * What is left of the test budget for the next evaluate_batch: judge calls not
 * yet used, and USD not yet spent by attest or the agent, each floored at 0.
 * Undefined when the budget sets no limit.
 */
export function remainingBudget(
  budget: TestBudget,
  usage: Usage,
  agentSpendUsd: number,
): BatchBudget | undefined {
  const wire: { max_judge_calls?: number; max_cost_usd?: number } = {};
  if (budget.maxJudgeCalls !== undefined) {
    wire.max_judge_calls = Math.max(0, budget.maxJudgeCalls - usage.judge_calls);
  }
  if (budget.maxCostUsd !== undefined) {
    wire.max_cost_usd = Math.max(0, budget.maxCostUsd - (usage.cost_usd + agentSpendUsd));
  }
  return Object.keys(wire).length === 0 ? undefined : wire;
}

/** True when attest cost plus agent spend has reached the test's cost limit. */
export function costBudgetSpent(budget: TestBudget | undefined, state: AttemptState): boolean {
  return (
    budget?.maxCostUsd !== undefined && state.usage.cost_usd + state.agentSpendUsd >= budget.maxCostUsd
  );
}

/** Count the agent's own spend of a trace once per attempt. */
export function recordAgentSpend(test: RunningTest, trace: Trace): void {
  const state = attemptState(test);
  if (state.traceIds.has(trace.trace_id)) return;
  state.traceIds.add(trace.trace_id);
  state.agentSpendUsd += trace.metadata?.cost_usd ?? 0;
}

/**
 * The meta of the running attempt. Results from an earlier attempt of a retried
 * test are dropped, so the report matches the outcome Vitest reports; their
 * usage is kept, because those model calls were made.
 */
export function attemptMeta(test: RunningTest): AttestTaskMeta {
  const attempt = attemptOf(test);
  let meta = test.meta.attest;
  if (meta === undefined || meta.attempt !== attempt) {
    meta = {
      attempt,
      results: [],
      totalCost: 0,
      totalDurationMs: 0,
      usage: meta?.usage ?? zeroUsage(),
      // The engine session passed its soft-failure budget in an earlier attempt too.
      ...(meta?.budgetExceeded === true ? { budgetExceeded: true } : {}),
    };
    test.meta.attest = meta;
  }
  return meta;
}

/**
 * Attach the results to the test's meta so AttestReporter, in the main process,
 * can write them with the test's file and line; rows of trial i carry `trial`.
 */
export function recordOnTest(test: RunningTest, result: AgentResult, trial: number | undefined): void {
  const state = attemptState(test);
  state.usage = addUsage(state.usage, result.usage);
  const meta = attemptMeta(test);
  let traceId: string | undefined;
  if (reportTracesEnabled()) {
    meta.traces ??= {};
    traceId = new TraceTable(meta.traces).add(result.trace);
  }
  meta.results.push(
    ...result.assertionResults.map((r) => ({
      ...r,
      ...(trial === undefined ? {} : { trial }),
      ...(traceId === undefined ? {} : { trace_id: traceId }),
    })),
  );
  meta.totalCost += result.totalCost;
  meta.totalDurationMs += result.totalDurationMs;
  meta.usage = addUsage(meta.usage, result.usage);
}

/**
 * Note that a batch returned `budget_exceeded`: on the test's meta when there is a test, else in
 * a marker file AttestReporter finds in its usage directory. The report ORs it across batches.
 */
export function recordBudgetExceeded(test: RunningTest | undefined): void {
  if (test !== undefined) {
    attemptMeta(test).budgetExceeded = true;
    return;
  }
  const dir = process.env[USAGE_DIR_ENV];
  if (dir === undefined) return;
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, `${randomUUID()}${BUDGET_MARKER_EXT}`), "");
}

export function strictFromEnv(): boolean {
  const v = (process.env["ATTEST_STRICT"] ?? "").trim().toLowerCase();
  return v === "1" || v === "true" || v === "yes";
}
