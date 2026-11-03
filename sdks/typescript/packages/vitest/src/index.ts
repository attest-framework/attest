export { attestGlobalSetup } from "./plugin.js";
export type { AttestGlobalSetupOptions } from "./plugin.js";
export { AttestEngineFixture, useAttest, evaluate, inspect, enforceOutcome } from "./fixtures.js";
export type { EvaluateOptions, InspectOptions } from "./fixtures.js";
export { trials, MIN_PASS_RATE_ENV } from "./trials.js";
export type { TrialsOptions } from "./trials.js";
export type { TestBudget } from "./test-state.js";
export { AttestCostReporter, AttestReporter } from "./reporter.js";
export type { AttestReporterOptions } from "./reporter.js";
export { buildReport, REPORT_VERSION, sessionUsage, summaryLines } from "./report.js";
export type { AttestTaskMeta, RecordedResult, TestOutcome, TestRecord, TrialsMeta } from "./report.js";
export { filterByTier } from "./tier-filter.js";
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
  uninstallEgressGuard,
} from "@attest-ai/core";
export type { ReplaySession, ReplaySummary } from "@attest-ai/core";
