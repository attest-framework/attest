export * from "./proto/index.js";
export type {
  AssertionSpec,
  SchemaSpec,
  ConstraintSpec,
  TraceSpec,
  ContentSpec,
  EmbeddingSpec,
  LlmJudgeSpec,
  TraceTreeSpec,
  PluginSpec,
  TraceId,
  AssertionId,
  AgentId,
  DriftReport,
  ConversationMessage,
  SimulatePersona,
  SimulateFaultConfig,
} from "./proto/types.js";
export { traceId, assertionId, agentId } from "./proto/types.js";
export { VERSION, ENGINE_VERSION } from "./version.js";
export { EngineManager } from "./engine-manager.js";
export { AttestClient, requireSimulationCapability } from "./client.js";
export type { AttestClientOptions } from "./client.js";
export { TraceBuilder } from "./trace.js";
export { AgentResult } from "./result.js";
export { addUsage, formatUsage, parseUsage, sumUsage, usageSummaryLine, zeroUsage } from "./usage.js";
export {
  renderAssertionFailure,
  renderDiagnostics,
  renderSummary,
} from "./diagnostics.js";
export type { RenderOptions } from "./diagnostics.js";
export { TraceTree } from "./trace-tree.js";
export { ExpectChain, WorldChecks, attestExpect, setChainObserver } from "./expect.js";
export {
  AssertionIdAssigner,
  ID_SCHEME,
  assertionIdOf,
  canonicalIdInput,
  canonicalSpec,
} from "./assertion-id.js";
export {
  CaptureError,
  loadFixture,
  record,
  replayRun,
  emitVitest,
  renderVitest,
  dslCall,
  testPathFor,
} from "./capture.js";
export type {
  Capture,
  Contract,
  ContractResult,
  Fixture,
  RecordOptions,
  RecordResult,
  ReplayRunOptions,
  Rerun,
} from "./capture.js";
export {
  ReplayError,
  ReplayMissError,
  ReplayNotEvaluatedError,
  firstMiss,
  liveLine,
  resolveMode,
  startReplay,
} from "./replay.js";
export { WorldError, startWorld } from "./world.js";
export type { WorldOptions, WorldSession, WorldSnapshot } from "./world.js";
export type {
  ReplayMode,
  ReplayModeSetting,
  ReplayOptions,
  ReplayRecord,
  ReplaySession,
  ReplaySummary,
} from "./replay.js";
export {
  EgressBlockedError,
  egressBlockRequested,
  installEgressGuard,
  isProviderHost,
  uninstallEgressGuard,
} from "./egress.js";
export { Agent, agent } from "./agent.js";
export { delegate } from "./delegate.js";
export { activeBuilder } from "./context.js";
export { TIER_1, TIER_2, TIER_3, tier } from "./tier.js";
export {
  config,
  isSimulationMode,
  simulationEnvEnabled,
  getSampleRate,
  resetConfig,
} from "./config.js";
export type { AttestPlugin, PluginResult } from "./plugins.js";
export {
  PluginRegistry,
  executePluginAssertion,
  loadPackagePlugins,
  registerPlugin,
  submitPluginResults,
} from "./plugins.js";
export { ContinuousEvalRunner, Sampler, AlertDispatcher } from "./continuous.js";
export {
  computeAgreement,
  loadLabelsCSV,
  loadLabelsJSONL,
  promptHash,
} from "./calibration.js";
export type { AgreementResult, LabelPair, LabeledRecord } from "./calibration.js";
export {
  FRIENDLY_USER,
  ADVERSARIAL_USER,
  CONFUSED_USER,
  COOPERATIVE_USER,
} from "./personas.js";
export {
  MockToolRegistry,
  RepeatResult,
  ScenarioResult,
  faultInject,
  mockTool,
  renderVerdictDiagnostics,
  repeat,
  scenario,
} from "./simulation/index.js";
export type {
  FaultInjectOptions,
  MockTool,
  MockToolFn,
  ScenarioConfig,
  ScenarioContext,
  ScenarioOptions,
  Verdict,
} from "./simulation/index.js";
export type { AlertPayload } from "./continuous.js";
export type { TraceAdapter } from "./adapters/index.js";
export {
  ManualAdapter,
  OpenAIAdapter,
  AnthropicAdapter,
  GeminiAdapter,
  OllamaAdapter,
  OTelAdapter,
  LangChainAdapter,
} from "./adapters/index.js";
