import type { Trace } from "../proto/types.js";
import { buildProviderTrace, type ProviderCallOptions } from "./provider-trace.js";

interface OllamaResponse {
  model?: string;
  message?: {
    content?: string;
    tool_calls?: { function?: { name?: string; arguments?: Record<string, unknown> } }[];
  };
  eval_count?: number;
  prompt_eval_count?: number;
}

export class OllamaAdapter {
  private readonly agentId: string | undefined;

  constructor(agentId?: string) {
    this.agentId = agentId;
  }

  traceFromResponse(
    response: OllamaResponse,
    options?: Omit<ProviderCallOptions, "costUsd">,
  ): Trace {
    const completion = response.message?.content ?? "";
    const counted = response.eval_count !== undefined && response.prompt_eval_count !== undefined;
    const input = options?.inputMessages ? { messages: options.inputMessages } : undefined;
    return buildProviderTrace(
      this.agentId,
      {
        model: response.model,
        completion,
        totalTokens: counted ? response.eval_count! + response.prompt_eval_count! : undefined,
        inputTokens: counted ? response.prompt_eval_count : undefined,
        outputTokens: counted ? response.eval_count : undefined,
        toolCalls: (response.message?.tool_calls ?? []).map((tc) => ({
          name: tc.function?.name ?? "",
          args: tc.function?.arguments ?? {},
        })),
      },
      options,
      input,
      { message: completion },
    );
  }
}
