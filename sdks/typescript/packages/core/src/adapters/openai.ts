import type { Trace } from "../proto/types.js";
import {
  buildProviderTrace,
  type ExtractedCall,
  type ProviderCallOptions,
} from "./provider-trace.js";

interface ChatUsage {
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
}

interface ChatToolCall {
  function: { name: string; arguments: string };
}

interface ChatMessage {
  content: string | null;
  refusal?: string | null;
  tool_calls?: ChatToolCall[];
}

/** An OpenAI Chat Completions response. */
interface OpenAIChatCompletion {
  object?: string;
  model?: string;
  usage?: ChatUsage;
  choices: { message: ChatMessage }[];
}

type ResponsesContentPart =
  | { type: "output_text"; text: string }
  | { type: "refusal"; refusal: string };

type ResponsesOutputItem =
  | { type: "message"; content: ResponsesContentPart[] }
  | { type: "function_call"; name: string; arguments: string }
  | { type: string };

/** An OpenAI Responses API response. */
interface OpenAIResponse {
  object: "response";
  model?: string;
  usage?: { input_tokens?: number; output_tokens?: number; total_tokens?: number };
  output: ResponsesOutputItem[];
}

function parseArguments(raw: string): Record<string, unknown> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { raw_arguments: raw };
  }
  if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
    return parsed as Record<string, unknown>;
  }
  return { raw_arguments: raw };
}

function isResponses(response: OpenAIChatCompletion | OpenAIResponse): response is OpenAIResponse {
  return response.object === "response";
}

function fromResponses(response: OpenAIResponse): ExtractedCall {
  const texts: string[] = [];
  const refusals: string[] = [];
  const toolCalls: ExtractedCall["toolCalls"] = [];
  for (const item of response.output) {
    if (item.type === "message") {
      for (const part of (item as { content: ResponsesContentPart[] }).content) {
        if (part.type === "output_text") texts.push(part.text);
        else if (part.type === "refusal") refusals.push(part.refusal);
      }
    } else if (item.type === "function_call") {
      const call = item as { name: string; arguments: string };
      toolCalls.push({ name: call.name, args: parseArguments(call.arguments) });
    }
  }
  const refusal = refusals.join("") || undefined;
  return {
    model: response.model,
    completion: texts.join("") || refusal || "",
    refusal,
    totalTokens: response.usage?.total_tokens,
    inputTokens: response.usage?.input_tokens,
    outputTokens: response.usage?.output_tokens,
    toolCalls,
  };
}

function fromChat(response: OpenAIChatCompletion): ExtractedCall {
  const message = response.choices[0].message;
  const refusal = message.refusal || undefined;
  return {
    model: response.model,
    completion: message.content || refusal || "",
    refusal,
    totalTokens: response.usage?.total_tokens,
    inputTokens: response.usage?.prompt_tokens,
    outputTokens: response.usage?.completion_tokens,
    toolCalls: (message.tool_calls ?? []).map((tc) => ({
      name: tc.function.name,
      args: parseArguments(tc.function.arguments),
    })),
  };
}

/**
 * Captures OpenAI Chat Completions and Responses API calls into Attest traces.
 *
 * A model refusal is the response's message: it becomes the completion and the trace
 * output, and is also recorded as `refusal` on the llm_call step.
 */
export class OpenAIAdapter {
  private readonly agentId: string | undefined;

  constructor(agentId?: string) {
    this.agentId = agentId;
  }

  traceFromResponse(
    response: OpenAIChatCompletion | OpenAIResponse,
    options?: ProviderCallOptions & { structuredOutput?: Record<string, unknown> },
  ): Trace {
    const call = isResponses(response) ? fromResponses(response) : fromChat(response);
    const input = options?.inputMessages ? { messages: options.inputMessages } : undefined;
    return buildProviderTrace(this.agentId, call, options, input, {
      message: call.completion,
      structured: options?.structuredOutput ?? {},
    });
  }
}
