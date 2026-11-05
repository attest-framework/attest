import type { Trace } from "../proto/types.js";
import { STEP_LLM_CALL } from "../proto/constants.js";
import { TraceBuilder } from "../trace.js";

/** Options every single-call provider adapter accepts. */
export interface ProviderCallOptions {
  inputMessages?: Record<string, unknown>[];
  costUsd?: number;
  /** Explicit latency; wins over the span between `startedAtMs` and `endedAtMs`. */
  latencyMs?: number;
  /** Wall-clock ms when the request was sent. */
  startedAtMs?: number;
  /** Wall-clock ms when the response was received. */
  endedAtMs?: number;
}

/** What an adapter extracts from one provider response. */
export interface ExtractedCall {
  model?: string;
  completion: string;
  refusal?: string;
  totalTokens?: number;
  inputTokens?: number;
  outputTokens?: number;
  toolCalls: { name: string; args: Record<string, unknown> }[];
}

function latency(options: ProviderCallOptions | undefined): number | undefined {
  if (options?.latencyMs !== undefined) return options.latencyMs;
  if (options?.startedAtMs !== undefined && options.endedAtMs !== undefined) {
    return options.endedAtMs - options.startedAtMs;
  }
  return undefined;
}

/**
 * Build the canonical single-call trace: input, one `completion` llm_call step carrying
 * the token counts, one tool_call step per tool call (in response order), output, metadata.
 */
export function buildProviderTrace(
  agentId: string | undefined,
  call: ExtractedCall,
  options: ProviderCallOptions | undefined,
  input: Record<string, unknown> | undefined,
  output: Record<string, unknown>,
): Trace {
  const builder = new TraceBuilder(agentId);
  if (input !== undefined) builder.setInput(input);

  const now = Date.now();
  const args: Record<string, unknown> = {};
  if (call.model !== undefined) args.model = call.model;
  const result: Record<string, unknown> = { completion: call.completion };
  if (call.refusal) result.refusal = call.refusal;
  if (call.totalTokens !== undefined) result.tokens = call.totalTokens;
  if (call.inputTokens !== undefined) result.input_tokens = call.inputTokens;
  if (call.outputTokens !== undefined) result.output_tokens = call.outputTokens;
  builder.addStep({
    type: STEP_LLM_CALL,
    name: "completion",
    args,
    result,
    started_at_ms: options?.startedAtMs ?? now,
    ended_at_ms: options?.endedAtMs ?? now,
  });
  for (const tc of call.toolCalls) {
    builder.addToolCall(tc.name, { args: tc.args });
  }

  builder.setOutput(output);
  builder.setMetadata({
    total_tokens: call.totalTokens,
    cost_usd: options?.costUsd,
    latency_ms: latency(options),
    model: call.model,
  });
  return builder.build();
}
