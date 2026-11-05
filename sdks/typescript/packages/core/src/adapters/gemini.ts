import type { Trace } from "../proto/types.js";
import { buildProviderTrace, type ProviderCallOptions } from "./provider-trace.js";

interface GeminiFunctionCall {
  name: string;
  args?: Record<string, unknown>;
}

interface GeminiPart {
  text?: string;
  /** Thought-summary parts are the model's reasoning, not the answer. */
  thought?: boolean;
  functionCall?: GeminiFunctionCall;
}

interface GeminiCandidate {
  content: { parts: GeminiPart[] };
}

interface GeminiUsage {
  promptTokenCount?: number;
  candidatesTokenCount?: number;
  totalTokenCount?: number;
}

interface GeminiResponse {
  text?: string;
  modelVersion?: string;
  usageMetadata?: GeminiUsage;
  candidates?: GeminiCandidate[];
}

function totalTokens(usage: GeminiUsage | undefined): number | undefined {
  if (usage === undefined) return undefined;
  if (usage.totalTokenCount !== undefined) return usage.totalTokenCount;
  if (usage.promptTokenCount !== undefined && usage.candidatesTokenCount !== undefined) {
    return usage.promptTokenCount + usage.candidatesTokenCount;
  }
  return usage.promptTokenCount;
}

/** A single string-content message is recorded as `{text}`, anything else as `{messages}`. */
function geminiInput(options: GeminiOptions | undefined): Record<string, unknown> | undefined {
  const messages = options?.inputMessages;
  if (messages && messages.length > 0) {
    const first = messages[0];
    if (messages.length === 1 && typeof first.content === "string") return { text: first.content };
    return { messages };
  }
  if (options?.inputText) return { text: options.inputText };
  return undefined;
}

interface GeminiOptions extends ProviderCallOptions {
  /** A plain-text prompt; `inputMessages` is the standard way to pass the input. */
  inputText?: string;
  /** Overrides the response's `modelVersion`. */
  model?: string;
}

export class GeminiAdapter {
  private readonly agentId: string | undefined;

  constructor(agentId?: string) {
    this.agentId = agentId;
  }

  traceFromResponse(response: GeminiResponse, options?: GeminiOptions): Trace {
    const parts = response.candidates?.[0]?.content.parts ?? [];
    const completion =
      response.text ??
      parts
        .filter((p) => p.text && !p.thought)
        .map((p) => p.text)
        .join("");

    const toolCalls = parts
      .filter((p) => p.functionCall)
      .map((p) => ({ name: p.functionCall!.name, args: p.functionCall!.args ?? {} }));

    const usage = response.usageMetadata;
    return buildProviderTrace(
      this.agentId,
      {
        model: options?.model ?? response.modelVersion,
        completion,
        totalTokens: totalTokens(usage),
        inputTokens: usage?.promptTokenCount,
        outputTokens: usage?.candidatesTokenCount,
        toolCalls,
      },
      options,
      geminiInput(options),
      { message: completion },
    );
  }
}
