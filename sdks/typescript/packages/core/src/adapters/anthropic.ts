import type { Trace } from "../proto/types.js";
import { buildProviderTrace, type ProviderCallOptions } from "./provider-trace.js";

interface AnthropicTextBlock {
  type: "text";
  text: string;
}

/** A client-executed (`tool_use`) or Anthropic-executed (`server_tool_use`) tool call. */
interface AnthropicToolUseBlock {
  type: "tool_use" | "server_tool_use";
  name: string;
  input: Record<string, unknown>;
}

/** Any other block (tool results, thinking, ...): carries no text or call of its own. */
interface AnthropicOtherBlock {
  type: string;
}

type AnthropicContentBlock = AnthropicTextBlock | AnthropicToolUseBlock | AnthropicOtherBlock;

interface AnthropicMessage {
  model?: string;
  usage?: { input_tokens: number; output_tokens: number };
  content: AnthropicContentBlock[];
}

export class AnthropicAdapter {
  private readonly agentId: string | undefined;

  constructor(agentId?: string) {
    this.agentId = agentId;
  }

  traceFromResponse(response: AnthropicMessage, options?: ProviderCallOptions): Trace {
    const texts: string[] = [];
    const toolCalls: { name: string; args: Record<string, unknown> }[] = [];
    for (const block of response.content) {
      if (block.type === "text") {
        texts.push((block as AnthropicTextBlock).text);
      } else if (block.type === "tool_use" || block.type === "server_tool_use") {
        const use = block as AnthropicToolUseBlock;
        toolCalls.push({ name: use.name, args: use.input });
      }
    }
    const completion = texts.join("\n");
    const usage = response.usage;
    const input = options?.inputMessages ? { messages: options.inputMessages } : undefined;
    return buildProviderTrace(
      this.agentId,
      {
        model: response.model,
        completion,
        totalTokens: usage ? usage.input_tokens + usage.output_tokens : undefined,
        inputTokens: usage?.input_tokens,
        outputTokens: usage?.output_tokens,
        toolCalls,
      },
      options,
      input,
      { message: completion },
    );
  }
}
