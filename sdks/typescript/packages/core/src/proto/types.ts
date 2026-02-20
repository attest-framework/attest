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
