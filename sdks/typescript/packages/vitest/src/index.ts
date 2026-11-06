export { attestGlobalSetup } from "./plugin.js";
export type { AttestGlobalSetupOptions } from "./plugin.js";
import { installChainGuard } from "./chain-guard.js";
export { attestPlugins, AttestEngineFixture, useAttest, evaluate, inspect, enforceOutcome } from "./fixtures.js";
export type { EvaluateOptions, InspectOptions } from "./fixtures.js";
export { trials, MIN_PASS_RATE_ENV } from "./trials.js";
export type { TrialsOptions } from "./trials.js";
export type { TestBudget } from "./test-state.js";
export { AttestReporter } from "./reporter.js";
export type { AttestReporterOptions } from "./reporter.js";
export { buildReport, REPORT_VERSION, sessionUsage, summaryLines } from "./report.js";
export type { AttestTaskMeta, RecordedResult, TestOutcome, TestRecord, TrialsMeta } from "./report.js";
export { filterByTier } from "./tier-filter.js";
export { useWorld } from "./world.js";
export { useReplay } from "./replay.js";
export type { ReplayHandle, ReplayMeta, UseReplayOptions } from "./replay.js";
export {
  EgressBlockedError,
  REASON_REPLAY_MISS,
  ReplayError,
  ReplayMissError,
  ReplayNotEvaluatedError,
  egressBlockRequested,
  firstMiss,
  installEgressGuard,
  isProviderHost,
  liveLine,
  resolveMode,
  startReplay,
  startWorld,
  uninstallEgressGuard,
  WorldError,
} from "@attest-ai/core";
export type { ReplaySession, ReplaySummary, WorldOptions, WorldSession, WorldSnapshot } from "@attest-ai/core";

// Importing the package turns on the check that fails a test with an unevaluated expect() chain.
installChainGuard();
