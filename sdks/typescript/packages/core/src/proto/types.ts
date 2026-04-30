// ---------------------------------------------------------------------------
// Branded ID types (T6)
// ---------------------------------------------------------------------------

declare const __brand: unique symbol;
type Brand<T, B extends string> = T & { readonly [__brand]: B };

export type TraceId = Brand<string, "TraceId">;
export type AssertionId = Brand<string, "AssertionId">;
export type AgentId = Brand<string, "AgentId">;

export function traceId(id: string): TraceId {
  return id as TraceId;
}

export function assertionId(id: string): AssertionId {
  return id as AssertionId;
}

export function agentId(id: string): AgentId {
  return id as AgentId;
}

// ---------------------------------------------------------------------------
// Assertion spec discriminated unions (T5)
// ---------------------------------------------------------------------------

export interface SchemaSpec {
  readonly type: "schema";
  readonly target: string;
  readonly schema: Record<string, unknown>;
}

export interface ConstraintSpec {
  readonly type: "constraint";
  readonly field: string;
  readonly operator: string;
  readonly value?: number;
  readonly min?: number;
  readonly max?: number;
  readonly soft: boolean;
}

export interface TraceSpec {
  readonly type: "trace";
  readonly check: string;
  readonly tools?: string[];
  readonly tool?: string;
  readonly max_repetitions?: number;
  readonly soft: boolean;
}

export interface ContentSpec {
  readonly type: "content";
  readonly target: string;
  readonly check: string;
  readonly value?: string;
  readonly values?: string[];
  readonly case_sensitive?: boolean;
  readonly soft?: boolean;
}

export interface EmbeddingSpec {
  readonly type: "embedding";
  readonly target: string;
  readonly reference: string;
  readonly threshold: number | "dynamic";
  readonly model?: string;
  readonly soft: boolean;
}

export interface LlmJudgeSpec {
  readonly type: "llm_judge";
  readonly target: string;
  readonly criteria: string;
  readonly rubric: string;
  readonly threshold: number | "dynamic";
  readonly model?: string;
  readonly soft: boolean;
}

export interface TraceTreeSpec {
  readonly type: "trace_tree";
  readonly check: string;
  readonly [key: string]: unknown;
}

export interface PluginSpec {
  readonly type: "plugin";
  readonly plugin_id: string;
  readonly config?: Record<string, unknown>;
  readonly soft: boolean;
}

export type AssertionSpec =
  | SchemaSpec
  | ConstraintSpec
  | TraceSpec
  | ContentSpec
  | EmbeddingSpec
  | LlmJudgeSpec
  | TraceTreeSpec
  | PluginSpec;

// ---------------------------------------------------------------------------
// Core protocol types
// ---------------------------------------------------------------------------

export interface TraceMetadata {
  readonly total_tokens?: number;
  readonly cost_usd?: number;
  readonly latency_ms?: number;
  readonly model?: string;
  readonly timestamp?: string;
}

export interface Step {
  readonly type: string;
  readonly name: string;
  readonly args?: Record<string, unknown>;
  readonly result?: Record<string, unknown>;
  readonly sub_trace?: Trace;
  readonly metadata?: Record<string, unknown>;
  readonly started_at_ms?: number;
  readonly ended_at_ms?: number;
  readonly agent_id?: string;
  readonly agent_role?: string;
}

export interface Trace {
  readonly trace_id: string;
  readonly output: Record<string, unknown>;
  readonly schema_version?: number;
  readonly agent_id?: string;
  readonly input?: Record<string, unknown>;
  readonly steps: readonly Step[];
  readonly metadata?: TraceMetadata;
  readonly parent_trace_id?: string;
}

export interface Assertion {
  readonly assertion_id: string;
  readonly type: string;
  readonly spec: Record<string, unknown>;
  readonly request_id?: string;
}

export interface AssertionResult {
  readonly assertion_id: string;
  readonly status: string;
  readonly score: number;
  readonly explanation: string;
  readonly cost?: number;
  readonly duration_ms?: number;
  readonly request_id?: string;
}

export interface ErrorData {
  readonly error_type: string;
  readonly retryable: boolean;
  readonly detail: string;
}

export interface RPCError {
  readonly code: number;
  readonly message: string;
  readonly data?: ErrorData;
}

export interface RPCRequest {
  readonly jsonrpc: "2.0";
  readonly id: number;
  readonly method: string;
  readonly params: Record<string, unknown>;
}

export interface RPCResponse {
  readonly jsonrpc: "2.0";
  readonly id: number;
  readonly result?: unknown;
  readonly error?: RPCError;
}

export interface InitializeParams {
  readonly sdk_name: string;
  readonly sdk_version: string;
  readonly protocol_version: number;
  readonly required_capabilities: readonly string[];
  readonly preferred_encoding?: string;
}

export interface InitializeResult {
  readonly engine_version: string;
  readonly protocol_version: number;
  readonly capabilities: readonly string[];
  readonly missing: readonly string[];
  readonly compatible: boolean;
  readonly encoding?: string;
  readonly max_concurrent_requests?: number;
  readonly max_trace_size_bytes?: number;
  readonly max_steps_per_trace?: number;
}

export interface EvaluateBatchParams {
  readonly trace: Trace;
  readonly assertions: readonly Assertion[];
}

export interface EvaluateBatchResult {
  readonly results: readonly AssertionResult[];
  readonly total_cost?: number;
  readonly total_duration_ms?: number;
  /**
   * True when this result was produced by an SDK simulation shim rather
   * than a real engine evaluation. Engine-produced results always carry
   * simulated=false. CI gates can fail builds that accidentally evaluate
   * against the simulator by checking this flag.
   */
  readonly simulated?: boolean;
}

export interface ShutdownResult {
  readonly sessions_completed: number;
  readonly assertions_evaluated: number;
}

export interface SubmitPluginResultParams {
  readonly trace_id: string;
  readonly plugin_name: string;
  readonly assertion_id: string;
  readonly result: AssertionResult;
}

// ---------------------------------------------------------------------------
// Drift detection types
// ---------------------------------------------------------------------------

export interface DriftReport {
  readonly assertion_id: string;
  readonly mean: number;
  readonly stddev: number;
  readonly count: number;
  readonly latest_score: number;
  readonly deviation: number;
  readonly status: string;
}

export interface QueryDriftParams {
  readonly assertion_id: string;
  readonly window_size: number;
}

export interface QueryDriftResult {
  readonly report: DriftReport;
}

// ---------------------------------------------------------------------------
// Simulation types
// ---------------------------------------------------------------------------

export interface SimulatePersona {
  readonly name: string;
  readonly system_prompt: string;
  readonly style: string;
  readonly temperature: number;
  readonly max_tokens?: number;
}

export interface SimulateFaultConfig {
  readonly error_rate: number;
  readonly latency_jitter_ms: number;
  readonly content_corruption: boolean;
  readonly timeout_after_ms: number;
}

export interface ConversationMessage {
  readonly role: string;
  readonly content: string;
}

export interface GenerateUserMessageParams {
  readonly persona: SimulatePersona;
  readonly conversation_history: readonly ConversationMessage[];
  readonly fault_config?: SimulateFaultConfig;
}

export interface GenerateUserMessageResult {
  readonly message: string;
}
