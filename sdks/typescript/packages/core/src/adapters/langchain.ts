import { createRequire } from "node:module";
import type { Step, Trace } from "../proto/types.js";
import { STEP_LLM_CALL, STEP_TOOL_CALL } from "../proto/constants.js";
import { TraceBuilder } from "../trace.js";

/**
 * The part of `@langchain/core`'s `BaseCallbackHandler` this adapter relies on. It is
 * declared locally so the published typings do not import an optional peer dependency.
 */
interface CallbackHandlerBase {
  name: string;
  awaitHandlers: boolean;
  raiseError: boolean;
}
type CallbackHandlerConstructor = new () => CallbackHandlerBase;

/** `@langchain/core` is an optional peer: resolve its handler base class when installed. */
function resolveBaseCallbackHandler(): CallbackHandlerConstructor {
  // `__filename` exists in the CommonJS build only; the ESM build resolves from its own URL.
  const load = createRequire(typeof __filename === "string" ? __filename : import.meta.url);
  try {
    const mod = load("@langchain/core/callbacks/base") as {
      BaseCallbackHandler: CallbackHandlerConstructor;
    };
    return mod.BaseCallbackHandler;
  } catch (cause) {
    if ((cause as NodeJS.ErrnoException).code !== "MODULE_NOT_FOUND") throw cause;
    return class MissingLangChain {
      constructor() {
        throw new Error(
          "LangChainAdapter requires @langchain/core: install it with `npm install @langchain/core`.",
        );
      }
    } as unknown as CallbackHandlerConstructor;
  }
}

// Dual-package caveat: this resolves the CJS `BaseCallbackHandler`, which an ESM consumer's
// `instanceof` check would not match. langchainjs dispatches by duck typing (handler methods
// and `ignore*` flags), never `instanceof`, so the capture is unaffected.
const BaseCallbackHandler = resolveBaseCallbackHandler();

type Values = Record<string, unknown>;

interface Message {
  content?: unknown;
  usage_metadata?: { input_tokens?: number; output_tokens?: number; total_tokens?: number };
}

interface LLMResult {
  generations: { text?: string; message?: Message }[][];
  llmOutput?: Values;
}

interface LLMStart {
  modelName: string | undefined;
  startedAtMs: number;
}

interface ToolStart {
  name: string;
  input: Values;
  startedAtMs: number;
}

function isValues(value: unknown): value is Values {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasContent(value: unknown): value is { content: unknown } {
  return isValues(value) && "content" in value;
}

/** Text of a message `content`: a string, or a list of strings and content blocks. */
function messageText(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    return content
      .map((block: unknown) => {
        if (typeof block === "string") return block;
        if (isValues(block) && block.type === "text") return String(block.text ?? "");
        return "";
      })
      .join("");
  }
  return String(content);
}

/** The first non-empty of `keys` in `values`, as JavaScript's `||` chain would pick it. */
function firstTruthy(values: Values, keys: string[]): unknown {
  for (const key of keys) if (values[key]) return values[key];
  return undefined;
}

function textOf(value: unknown): string {
  if (typeof value === "string") return value;
  if (hasContent(value)) return messageText(value.content);
  return value ? String(value) : "";
}

/** `invocation_params` of a chat-model start: nested under `extraParams` by langchainjs. */
function modelNameOf(extraParams: Values | undefined): string | undefined {
  if (extraParams === undefined) return undefined;
  const params = isValues(extraParams.invocation_params) ? extraParams.invocation_params : extraParams;
  const name = params.model_name ?? params.model ?? params.modelName;
  return typeof name === "string" && name ? name : undefined;
}

/** `serialized` is langchainjs's `Serialized` (no index signature), so it is narrowed here. */
function toolNameOf(serialized: unknown, runName: string | undefined): string {
  if (runName) return runName;
  if (isValues(serialized)) {
    if (typeof serialized.name === "string" && serialized.name) return serialized.name;
    if (Array.isArray(serialized.id) && serialized.id.length > 0) return String(serialized.id.at(-1));
  }
  return "unknown_tool";
}

/** A tool's structured input: langchainjs hands `handleToolStart` the JSON text of it. */
function toolInputOf(input: string): Values {
  try {
    const parsed: unknown = JSON.parse(input);
    if (isValues(parsed)) return parsed;
  } catch {
    // not JSON: recorded as the raw text below
  }
  return { input };
}

/**
 * Captures a LangChain.js run into an Attest trace.
 *
 * A `BaseCallbackHandler` from `@langchain/core`: pass it in `callbacks` and call
 * `buildTrace()` once the run resolves. Model and tool runs are keyed by `runId`, so
 * interleaved runs are attributed correctly; steps are recorded in completion order.
 *
 * ```ts
 * const adapter = new LangChainAdapter("my-agent");
 * await agent.invoke(input, { callbacks: [adapter] });
 * const trace = adapter.buildTrace();
 * ```
 */
export class LangChainAdapter extends BaseCallbackHandler {
  override name = "attest_langchain_adapter";

  private readonly agentId: string | undefined;
  private input: string | undefined;
  private output: string | undefined;
  private steps: Step[] = [];
  private llmStarts = new Map<string, LLMStart>();
  private toolStarts = new Map<string, ToolStart>();
  private totalTokens = 0;
  private model: string | undefined;
  private startedAtMs: number | undefined;
  private endedAtMs: number | undefined;

  constructor(agentId?: string) {
    super();
    this.agentId = agentId;
    // Callbacks run in the background by default; the trace must be complete when the
    // run resolves.
    this.awaitHandlers = true;
    this.raiseError = false;
  }

  /** Handlers are shared by the callback manager, never cloned: a clone would lose the capture. */
  copy(): this {
    return this;
  }

  /** Reset accumulated state for a new capture session. */
  reset(): void {
    this.input = undefined;
    this.output = undefined;
    this.steps = [];
    this.llmStarts.clear();
    this.toolStarts.clear();
    this.totalTokens = 0;
    this.model = undefined;
    this.startedAtMs = undefined;
    this.endedAtMs = undefined;
  }

  private now(): number {
    return Date.now();
  }

  handleChainStart(_chain: unknown, inputs: Values, _runId: string, parentRunId?: string): void {
    if (parentRunId !== undefined) return;
    this.startedAtMs = this.now();
    if (!isValues(inputs)) return;
    const message = firstTruthy(inputs, ["input", "messages", "query"]) ?? "";
    if (typeof message === "string") {
      this.input = message;
    } else if (Array.isArray(message) && message.length > 0) {
      const first: unknown = message[0];
      this.input = hasContent(first) ? messageText(first.content) : String(first);
    } else {
      this.input = message ? String(message) : "";
    }
  }

  handleChainEnd(outputs: Values, _runId: string, parentRunId?: string): void {
    if (parentRunId !== undefined) return;
    this.endedAtMs = this.now();
    if (!isValues(outputs)) return;
    // LangGraph returns {messages: [BaseMessage, ...]}: the last message is the answer.
    const messages = outputs.messages;
    if (Array.isArray(messages) && messages.length > 0) {
      const last: unknown = messages[messages.length - 1];
      if (hasContent(last)) {
        this.output = messageText(last.content);
        return;
      }
    }
    this.output = textOf(firstTruthy(outputs, ["output", "result", "text"]));
  }

  handleChatModelStart(
    _llm: unknown,
    _messages: unknown[][],
    runId: string,
    _parentRunId?: string,
    extraParams?: Values,
  ): void {
    this.llmStarts.set(runId, { modelName: modelNameOf(extraParams), startedAtMs: this.now() });
  }

  handleLLMStart(
    _llm: unknown,
    _prompts: string[],
    runId: string,
    _parentRunId?: string,
    extraParams?: Values,
  ): void {
    this.llmStarts.set(runId, { modelName: modelNameOf(extraParams), startedAtMs: this.now() });
  }

  handleLLMEnd(output: LLMResult, runId: string): void {
    const start = this.llmStarts.get(runId);
    this.llmStarts.delete(runId);
    const endedAtMs = this.now();
    let modelName = start?.modelName;

    // Token usage: the message's usage_metadata (current langchain-core), else the
    // provider's llmOutput.tokenUsage (older integrations).
    let inputTokens = 0;
    let outputTokens = 0;
    let total = 0;
    const usage = output.generations.flat().find((g) => g.message?.usage_metadata)?.message
      ?.usage_metadata;
    if (usage) {
      inputTokens = usage.input_tokens || 0;
      outputTokens = usage.output_tokens || 0;
      total = usage.total_tokens || inputTokens + outputTokens;
    }
    const llmOutput = output.llmOutput;
    if (llmOutput) {
      if (!usage) {
        const tokenUsage = (llmOutput.tokenUsage ?? llmOutput.token_usage ?? {}) as Values;
        inputTokens = Number(tokenUsage.promptTokens ?? tokenUsage.prompt_tokens ?? 0);
        outputTokens = Number(tokenUsage.completionTokens ?? tokenUsage.completion_tokens ?? 0);
        total = inputTokens + outputTokens;
      }
      const reported = llmOutput.model_name ?? llmOutput.modelName;
      if (modelName === undefined && typeof reported === "string") modelName = reported;
    }

    this.totalTokens += total;
    if (modelName && this.model === undefined) this.model = modelName;

    const args: Values = {};
    if (modelName) args.model = modelName;
    const result: Values = {};
    const completion = output.generations[0]?.[0]?.text;
    if (completion) result.completion = completion;
    if (total) result.tokens = total;
    if (inputTokens) result.input_tokens = inputTokens;
    if (outputTokens) result.output_tokens = outputTokens;

    this.steps.push(this.step(STEP_LLM_CALL, modelName || "llm", args, result, start?.startedAtMs, endedAtMs));
  }

  handleToolStart(
    tool: unknown,
    input: string,
    runId: string,
    _parentRunId?: string,
    _tags?: string[],
    _metadata?: Values,
    runName?: string,
  ): void {
    this.toolStarts.set(runId, {
      name: toolNameOf(tool, runName),
      input: toolInputOf(input),
      startedAtMs: this.now(),
    });
  }

  handleToolEnd(output: unknown, runId: string): void {
    // LangGraph passes ToolMessage objects; their content is text or content blocks.
    const text = hasContent(output) ? messageText(output.content) : String(output);
    // A ToolMessage carries the tool's own name whenever the call has a tool-call id;
    // `runName` / `serialized` name a StructuredTool subclass after its class. The error
    // path has no ToolMessage, so a failed custom tool keeps the class-derived name.
    const toolName = isValues(output) && typeof output.name === "string" && output.name ? output.name : undefined;
    this.finishTool(runId, { output: text }, toolName);
  }

  handleToolError(error: unknown, runId: string): void {
    const message = error instanceof Error ? error.message : String(error);
    this.finishTool(runId, { error: message }, undefined);
  }

  private finishTool(runId: string, result: Values, endName: string | undefined): void {
    const start = this.toolStarts.get(runId);
    this.toolStarts.delete(runId);
    this.steps.push(
      this.step(
        STEP_TOOL_CALL,
        endName ?? start?.name ?? "unknown_tool",
        start?.input ?? {},
        result,
        start?.startedAtMs,
        this.now(),
      ),
    );
  }

  private step(
    type: string,
    name: string,
    args: Values,
    result: Values,
    startedAtMs: number | undefined,
    endedAtMs: number,
  ): Step {
    return {
      type,
      name,
      args,
      result,
      metadata: startedAtMs === undefined ? {} : { duration_ms: endedAtMs - startedAtMs },
      started_at_ms: startedAtMs,
      ended_at_ms: endedAtMs,
      agent_id: this.agentId,
    };
  }

  /**
   * Build a Trace from the captured callback events.
   *
   * @param options.costUsd - Total cost.
   * @param options.model - Model name override.
   */
  buildTrace(options?: { costUsd?: number; model?: string }): Trace {
    const builder = new TraceBuilder(this.agentId);
    if (this.input) builder.setInput({ message: this.input });
    for (const step of this.steps) builder.addStep(step);
    builder.setOutput({ message: this.output ?? "" });
    builder.setMetadata({
      total_tokens: this.totalTokens > 0 ? this.totalTokens : undefined,
      cost_usd: options?.costUsd,
      latency_ms:
        this.startedAtMs !== undefined && this.endedAtMs !== undefined
          ? this.endedAtMs - this.startedAtMs
          : undefined,
      model: options?.model ?? this.model,
    });
    return builder.build();
  }
}
