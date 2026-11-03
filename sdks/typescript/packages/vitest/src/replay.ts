/**
 * `useReplay(name)`: run the test's real agent code with model responses served from
 * fixture `<name>` by the engine's replay proxy, and report how each request was served.
 */
import { onTestFinished } from "vitest";
import {
  ReplayMissError,
  egressBlockRequested,
  firstMiss,
  installEgressGuard,
  resolveReplayMode,
  startReplay,
  type ReplayModeSetting,
  type ReplayOptions,
  type ReplaySession,
  type ReplaySummary,
} from "@attest-ai/core";
import { strictFromEnv } from "./test-state.js";

/** Replay provenance of one test, carried from the worker in task meta. */
export interface ReplayMeta {
  fixtures: string[];
  mode: "strict" | "auto";
  replayed: number;
  live: number;
  misses: number;
  /** First miss message. */
  miss?: string;
  liveCostUsd: number;
  unpricedLiveCalls: number;
  /** The test was skipped as `not_evaluated: replay_miss`. */
  notEvaluated: boolean;
}

export interface UseReplayOptions extends Omit<ReplayOptions, "mode"> {
  /** `strict`, `auto` or `off`; default `ATTEST_REPLAY`, else `strict` when `CI` is set, else `auto`. */
  mode?: ReplayModeSetting;
  /** Block in-process connections to model-provider hosts (default: `ATTEST_EGRESS=block`). */
  egress?: "block" | "allow";
  /** Fail (instead of skip) when auto mode misses. Default: the ATTEST_STRICT variable. */
  strict?: boolean;
}

export interface ReplayHandle {
  /** `off` when replay is disabled and nothing was started. */
  readonly mode: "strict" | "auto" | "off";
  /** The session's environment (empty when off). */
  readonly env: Record<string, string>;
  /** Stop the replay now and return its summary (`undefined` when off). The test's outcome is still decided at its end. */
  finish(): Promise<ReplaySummary | undefined>;
}

interface FinishedTask {
  name: string;
  meta: Record<string, unknown>;
  result?: { state?: string; errors?: unknown[] };
}

/**
 * Start replaying fixture `name` for the running test. Call it before constructing the
 * model client (clients read `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` at construction).
 * When the test ends the replay stops and the test's outcome follows the summary:
 * - strict: any miss fails the test with the miss message, whatever the agent threw;
 * - auto: a miss with no provider key skips the test as `not evaluated: replay_miss`
 *   (a failure under `strict: true` / `ATTEST_STRICT=1`); with a key the call went live.
 */
export async function useReplay(name: string, opts: UseReplayOptions = {}): Promise<ReplayHandle> {
  const mode = resolveReplayMode(opts.mode);
  if (mode === "off") return { mode, env: {}, finish: () => Promise.resolve(undefined) };

  const uninstallGuard = (opts.egress ?? (egressBlockRequested() ? "block" : "allow")) === "block" ? installEgressGuard() : undefined;
  let session: ReplaySession;
  try {
    session = await startReplay(name, { ...opts, mode });
  } catch (err) {
    uninstallGuard?.();
    throw err;
  }
  let summary: ReplaySummary | undefined;
  const finish = async (): Promise<ReplaySummary> => {
    try {
      summary = await session.finish();
      return summary;
    } finally {
      uninstallGuard?.();
    }
  };
  onTestFinished(async ({ task }) => {
    const done = await finish();
    settle(task as unknown as FinishedTask, name, mode, done, opts.strict ?? strictFromEnv());
  });
  return { mode, env: session.env, finish };
}

function settle(task: FinishedTask, name: string, mode: "strict" | "auto", s: ReplaySummary, strict: boolean): void {
  const miss = firstMiss(s) ?? undefined;
  const skip = s.misses > 0 && mode === "auto" && !strict;
  const meta: ReplayMeta = {
    fixtures: [name],
    mode,
    replayed: s.replayed,
    live: s.live,
    misses: s.misses,
    ...(miss !== undefined ? { miss } : {}),
    liveCostUsd: s.live_cost_usd,
    unpricedLiveCalls: s.unpriced_live_calls,
    // An auto miss is a not_evaluated row in the report even when --strict fails the test.
    notEvaluated: mode === "auto" && s.misses > 0,
  };
  const prior = task.meta["attestReplay"] as ReplayMeta | undefined;
  task.meta["attestReplay"] = prior === undefined ? meta : mergeMeta(prior, meta);
  if (miss === undefined) return;
  if (skip) {
    // The agent's own error is a consequence of the miss; the test is not evaluated.
    task.result = { ...task.result, state: "skip", errors: undefined };
    return;
  }
  const error = new ReplayMissError(miss, s);
  if (task.result?.errors !== undefined && task.result.errors.length > 0) {
    // Whatever the agent threw, the user sees the miss.
    task.result.errors = [error];
    task.result.state = "fail";
    return;
  }
  throw error;
}

/** Fold a later session of the same test into the earlier one, as the Python report does. */
function mergeMeta(a: ReplayMeta, b: ReplayMeta): ReplayMeta {
  const miss = a.miss ?? b.miss;
  return {
    fixtures: [...new Set([...a.fixtures, ...b.fixtures])],
    mode: a.mode === b.mode ? a.mode : "auto",
    replayed: a.replayed + b.replayed,
    live: a.live + b.live,
    misses: a.misses + b.misses,
    ...(miss !== undefined ? { miss } : {}),
    liveCostUsd: a.liveCostUsd + b.liveCostUsd,
    unpricedLiveCalls: a.unpricedLiveCalls + b.unpricedLiveCalls,
    notEvaluated: a.notEvaluated || b.notEvaluated,
  };
}
