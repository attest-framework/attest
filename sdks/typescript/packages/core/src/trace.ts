import { randomUUID } from "node:crypto";
import type { Step, Trace, TraceMetadata } from "./proto/types.js";
import {
  STEP_LLM_CALL,
  STEP_TOOL_CALL,
  STEP_RETRIEVAL,
} from "./proto/constants.js";

export class TraceBuilder {
  private traceId: string;
  private agentId: string | undefined;
  private input: Record<string, unknown> | undefined;
  private readonly steps: Step[] = [];
  private output: Record<string, unknown> | undefined;
  private metadata: TraceMetadata | undefined;
  private parentTraceId: string | undefined;

  constructor(agentId?: string) {
    this.traceId = `trc_${randomUUID().replace(/-/g, "").slice(0, 12)}`;
    this.agentId = agentId;
  }

  setTraceId(traceId: string): this {
    this.traceId = traceId;
    return this;
  }

  setInput(input: Record<string, unknown>): this {
    this.input = input;
    return this;
  }

  addLlmCall(
    name: string,
    options?: {
      args?: Record<string, unknown>;
      result?: Record<string, unknown>;
      metadata?: Record<string, unknown>;
    },
  ): this {
    this.steps.push({
      type: STEP_LLM_CALL,
      name,
      args: options?.args,
      result: options?.result,
      metadata: options?.metadata,
    });
    return this;
  }

  addToolCall(
    name: string,
    options?: {
      args?: Record<string, unknown>;
      result?: Record<string, unknown>;
      metadata?: Record<string, unknown>;
    },
  ): this {
    this.steps.push({
      type: STEP_TOOL_CALL,
      name,
      args: options?.args,
      result: options?.result,
      metadata: options?.metadata,
    });
    return this;
  }

  addRetrieval(
    name: string,
    options?: {
      args?: Record<string, unknown>;
      result?: Record<string, unknown>;
      metadata?: Record<string, unknown>;
    },
  ): this {
    this.steps.push({
      type: STEP_RETRIEVAL,
      name,
      args: options?.args,
      result: options?.result,
      metadata: options?.metadata,
    });
    return this;
  }

  addStep(step: Step): this {
    this.steps.push(step);
    return this;
  }

  setOutput(output: Record<string, unknown>): this {
    this.output = output;
    return this;
  }

  setMetadata(metadata: {
    total_tokens?: number;
    cost_usd?: number;
    latency_ms?: number;
    model?: string;
    timestamp?: string;
  }): this {
    this.metadata = metadata;
    return this;
  }

  getTraceId(): string {
    return this.traceId;
  }

  setParentTraceId(parentId: string): this {
    this.parentTraceId = parentId;
    return this;
  }

  build(): Trace {
    if (this.output === undefined) {
      throw new Error("Trace output is required. Call setOutput() before build().");
    }
    return {
      trace_id: this.traceId,
      output: this.output,
      schema_version: 1,
      agent_id: this.agentId,
      input: this.input,
      steps: [...this.steps],
      metadata: this.metadata,
      parent_trace_id: this.parentTraceId,
    };
  }
}
