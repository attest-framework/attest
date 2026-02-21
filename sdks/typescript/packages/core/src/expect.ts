import { randomUUID } from "node:crypto";
import type { Assertion, Trace } from "./proto/types.js";
import {
  TYPE_CONSTRAINT,
  TYPE_CONTENT,
  TYPE_EMBEDDING,
  TYPE_LLM_JUDGE,
  TYPE_SCHEMA,
  TYPE_TRACE,
  TYPE_TRACE_TREE,
} from "./proto/constants.js";
import { AgentResult } from "./result.js";

export class ExpectChain {
  private readonly _result: AgentResult;
  private readonly _assertions: Assertion[] = [];

  constructor(result: AgentResult) {
    this._result = result;
  }

  get assertions(): Assertion[] {
    return [...this._assertions];
  }

  get trace(): Trace {
    return this._result.trace;
  }

  private add(assertionType: string, spec: Record<string, unknown>): this {
    this._assertions.push({
      assertion_id: `assert_${randomUUID().replace(/-/g, "").slice(0, 8)}`,
      type: assertionType,
      spec,
    });
    return this;
  }

  // -- Layer 1: Schema --

  outputMatchesSchema(schema: Record<string, unknown>): this {
    return this.add(TYPE_SCHEMA, { target: "output.structured", schema });
  }

  outputFieldMatchesSchema(field: string, schema: Record<string, unknown>): this {
    return this.add(TYPE_SCHEMA, { target: `output.${field}`, schema });
  }

  toolArgsMatchSchema(toolName: string, schema: Record<string, unknown>): this {
    return this.add(TYPE_SCHEMA, {
      target: `steps[?name=='${toolName}'].args`,
      schema,
    });
  }

  toolResultMatchesSchema(toolName: string, schema: Record<string, unknown>): this {
    return this.add(TYPE_SCHEMA, {
      target: `steps[?name=='${toolName}'].result`,
      schema,
    });
  }

  // -- Layer 2: Constraints --

  costUnder(maxCost: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "metadata.cost_usd",
      operator: "lte",
      value: maxCost,
      soft: opts?.soft ?? false,
    });
  }

  latencyUnder(maxMs: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "metadata.latency_ms",
      operator: "lte",
      value: maxMs,
      soft: opts?.soft ?? false,
    });
  }

  tokensUnder(maxTokens: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "metadata.total_tokens",
      operator: "lte",
      value: maxTokens,
      soft: opts?.soft ?? false,
    });
  }

  tokensBetween(minTokens: number, maxTokens: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "metadata.total_tokens",
      operator: "between",
      min: minTokens,
      max: maxTokens,
      soft: opts?.soft ?? false,
    });
  }

  stepCount(operator: string, value: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "steps.length",
      operator,
      value,
      soft: opts?.soft ?? false,
    });
  }

  toolCallCount(operator: string, value: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "steps[?type=='tool_call'].length",
      operator,
      value,
      soft: opts?.soft ?? false,
    });
  }

  constraint(
    field: string,
    operator: string,
    opts?: { value?: number; min?: number; max?: number; soft?: boolean },
  ): this {
    const spec: Record<string, unknown> = {
      field,
      operator,
      soft: opts?.soft ?? false,
    };
    if (opts?.value !== undefined) spec.value = opts.value;
    if (opts?.min !== undefined) spec.min = opts.min;
    if (opts?.max !== undefined) spec.max = opts.max;
    return this.add(TYPE_CONSTRAINT, spec);
  }

  // -- Layer 3: Trace --

  toolsCalledInOrder(tools: string[], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, {
      check: "contains_in_order",
      tools,
      soft: opts?.soft ?? false,
    });
  }

  toolsCalledExactly(tools: string[], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, {
      check: "exact_order",
      tools,
      soft: opts?.soft ?? false,
    });
  }

  noToolLoops(tool: string, maxRepetitions = 1, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, {
      check: "loop_detection",
      tool,
      max_repetitions: maxRepetitions,
      soft: opts?.soft ?? false,
    });
  }

  noDuplicateTools(opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, { check: "no_duplicates", soft: opts?.soft ?? false });
  }

  requiredTools(tools: string[], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, {
      check: "required_tools",
      tools,
      soft: opts?.soft ?? false,
    });
  }

  forbiddenTools(tools: string[], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, {
      check: "forbidden_tools",
      tools,
      soft: opts?.soft ?? false,
    });
  }

  // -- Layer 4: Content --

  outputContains(value: string, opts?: { caseSensitive?: boolean; soft?: boolean }): this {
    return this.add(TYPE_CONTENT, {
      target: "output.message",
      check: "contains",
      value,
      case_sensitive: opts?.caseSensitive ?? false,
      soft: opts?.soft ?? false,
    });
  }

  outputNotContains(value: string, opts?: { caseSensitive?: boolean; soft?: boolean }): this {
    return this.add(TYPE_CONTENT, {
      target: "output.message",
      check: "not_contains",
      value,
      case_sensitive: opts?.caseSensitive ?? false,
      soft: opts?.soft ?? false,
    });
  }

  outputMatchesRegex(pattern: string, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONTENT, {
      target: "output.message",
      check: "regex_match",
      value: pattern,
      soft: opts?.soft ?? false,
    });
  }

  outputHasAllKeywords(
    keywords: string[],
    opts?: { caseSensitive?: boolean; soft?: boolean },
  ): this {
    return this.add(TYPE_CONTENT, {
      target: "output.message",
      check: "keyword_all",
      values: keywords,
      case_sensitive: opts?.caseSensitive ?? false,
      soft: opts?.soft ?? false,
    });
  }

  outputHasAnyKeyword(
    keywords: string[],
    opts?: { caseSensitive?: boolean; soft?: boolean },
  ): this {
    return this.add(TYPE_CONTENT, {
      target: "output.message",
      check: "keyword_any",
      values: keywords,
      case_sensitive: opts?.caseSensitive ?? false,
      soft: opts?.soft ?? false,
    });
  }

  outputForbids(terms: string[]): this {
    return this.add(TYPE_CONTENT, {
      target: "output.message",
      check: "forbidden",
      values: terms,
    });
  }

  contentContains(
    target: string,
    value: string,
    opts?: { caseSensitive?: boolean; soft?: boolean },
  ): this {
    return this.add(TYPE_CONTENT, {
      target,
      check: "contains",
      value,
      case_sensitive: opts?.caseSensitive ?? false,
      soft: opts?.soft ?? false,
    });
  }

  // -- Layer 5: Embedding Similarity --

  outputSimilarTo(
    reference: string,
    opts?: { threshold?: number; model?: string; soft?: boolean },
  ): this {
    return this.add(TYPE_EMBEDDING, {
      target: "output.message",
      reference,
      threshold: opts?.threshold ?? 0.8,
      model: opts?.model,
      soft: opts?.soft ?? false,
    });
  }

  // -- Layer 6: LLM Judge --

  passesJudge(
    criteria: string,
    opts?: { rubric?: string; threshold?: number; model?: string; soft?: boolean },
  ): this {
    return this.add(TYPE_LLM_JUDGE, {
      target: "output.message",
      criteria,
      rubric: opts?.rubric ?? "default",
      threshold: opts?.threshold ?? 0.8,
      model: opts?.model,
      soft: opts?.soft ?? false,
    });
  }

  // -- Layer 7: Trace Tree (Multi-Agent) --

  agentCalled(agentId: string, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "agent_called",
      agent_id: agentId,
      soft: opts?.soft ?? false,
    });
  }

  delegationDepth(maxDepth: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "delegation_depth",
      max_depth: maxDepth,
      soft: opts?.soft ?? false,
    });
  }

  agentOutputContains(
    agentId: string,
    value: string,
    opts?: { caseSensitive?: boolean; soft?: boolean },
  ): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "agent_output_contains",
      agent_id: agentId,
      value,
      case_sensitive: opts?.caseSensitive ?? false,
      soft: opts?.soft ?? false,
    });
  }

  crossAgentDataFlow(
    fromAgent: string,
    toAgent: string,
    field: string,
    opts?: { soft?: boolean },
  ): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "cross_agent_data_flow",
      from_agent: fromAgent,
      to_agent: toAgent,
      field,
      soft: opts?.soft ?? false,
    });
  }

  followsTransitions(transitions: [string, string][], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "follows_transitions",
      transitions,
      soft: opts?.soft ?? false,
    });
  }

  aggregateCostUnder(maxCost: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "aggregate_cost",
      operator: "lte",
      value: maxCost,
      soft: opts?.soft ?? false,
    });
  }

  aggregateTokensUnder(maxTokens: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "aggregate_tokens",
      operator: "lte",
      value: maxTokens,
      soft: opts?.soft ?? false,
    });
  }

  agentOrderedBefore(agentA: string, agentB: string, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "agent_ordered_before",
      agent_a: agentA,
      agent_b: agentB,
      soft: opts?.soft ?? false,
    });
  }

  agentsOverlap(agentA: string, agentB: string, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "agents_overlap",
      agent_a: agentA,
      agent_b: agentB,
      soft: opts?.soft ?? false,
    });
  }

  agentWallTimeUnder(agentId: string, maxMs: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "agent_wall_time_under",
      agent_id: agentId,
      max_ms: maxMs,
      soft: opts?.soft ?? false,
    });
  }

  orderedAgents(groups: string[][], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "ordered_agents",
      groups,
      soft: opts?.soft ?? false,
    });
  }
}

/**
 * Create an assertion chain for an agent result.
 *
 * Accepts either an {@link AgentResult} or a raw {@link Trace}. When a
 * `Trace` is passed it is automatically wrapped in an `AgentResult`.
 */
export function attestExpect(result: AgentResult | Trace): ExpectChain {
  if (result instanceof AgentResult) {
    return new ExpectChain(result);
  }
  // Treat as Trace — wrap in AgentResult
  return new ExpectChain(new AgentResult(result));
}
