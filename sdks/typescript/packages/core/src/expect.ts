import { randomUUID } from "node:crypto";
import type { Assertion, Trace } from "./proto/types.js";
import {
  TYPE_CONSTRAINT,
  TYPE_CONTENT,
  TYPE_EMBEDDING,
  TYPE_LLM_JUDGE,
  TYPE_PLUGIN,
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

  /**
   * Assert that `output.structured` conforms to the given JSON Schema.
   *
   * @param schema - A JSON Schema object to validate against.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).outputMatchesSchema({ type: "object", required: ["answer"] });
   * ```
   */
  outputMatchesSchema(schema: Record<string, unknown>): this {
    return this.add(TYPE_SCHEMA, { target: "output.structured", schema });
  }

  /**
   * Assert that a specific output field conforms to the given JSON Schema.
   *
   * @param field - Dot-delimited path under `output` (e.g. `"structured.items"`).
   * @param schema - A JSON Schema object to validate against.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).outputFieldMatchesSchema("structured.items", { type: "array" });
   * ```
   */
  outputFieldMatchesSchema(field: string, schema: Record<string, unknown>): this {
    return this.add(TYPE_SCHEMA, { target: `output.${field}`, schema });
  }

  /**
   * Assert that a named tool's arguments conform to the given JSON Schema.
   *
   * @param toolName - The tool name to match in `steps`.
   * @param schema - A JSON Schema object to validate against.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).toolArgsMatchSchema("search", { type: "object", required: ["query"] });
   * ```
   */
  toolArgsMatchSchema(toolName: string, schema: Record<string, unknown>): this {
    return this.add(TYPE_SCHEMA, {
      target: `steps[?name=='${toolName}'].args`,
      schema,
    });
  }

  /**
   * Assert that a named tool's result conforms to the given JSON Schema.
   *
   * @param toolName - The tool name to match in `steps`.
   * @param schema - A JSON Schema object to validate against.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).toolResultMatchesSchema("search", { type: "object" });
   * ```
   */
  toolResultMatchesSchema(toolName: string, schema: Record<string, unknown>): this {
    return this.add(TYPE_SCHEMA, {
      target: `steps[?name=='${toolName}'].result`,
      schema,
    });
  }

  // -- Layer 2: Constraints --

  /**
   * Assert total cost (in USD) is at or below `maxCost`.
   *
   * @param maxCost - Maximum allowed cost in USD.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).costUnder(0.05);
   * expect(result).costUnder(0.05, { soft: true });
   * ```
   */
  costUnder(maxCost: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "metadata.cost_usd",
      operator: "lte",
      value: maxCost,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert total latency is at or below `maxMs` milliseconds.
   *
   * @param maxMs - Maximum allowed latency in milliseconds.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).latencyUnder(2000);
   * ```
   */
  latencyUnder(maxMs: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "metadata.latency_ms",
      operator: "lte",
      value: maxMs,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert total token usage is at or below `maxTokens`.
   *
   * @param maxTokens - Maximum allowed token count.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).tokensUnder(500);
   * ```
   */
  tokensUnder(maxTokens: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "metadata.total_tokens",
      operator: "lte",
      value: maxTokens,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert total token usage is between `minTokens` and `maxTokens` (inclusive).
   *
   * @param minTokens - Minimum allowed token count.
   * @param maxTokens - Maximum allowed token count.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).tokensBetween(100, 500);
   * ```
   */
  tokensBetween(minTokens: number, maxTokens: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "metadata.total_tokens",
      operator: "between",
      min: minTokens,
      max: maxTokens,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert the number of steps satisfies the given comparison.
   *
   * @param operator - Comparison operator (e.g. `"eq"`, `"lte"`, `"gte"`).
   * @param value - The reference count to compare against.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).stepCount("lte", 5);
   * ```
   */
  stepCount(operator: string, value: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "steps.length",
      operator,
      value,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert the number of tool-call steps satisfies the given comparison.
   *
   * @param operator - Comparison operator (e.g. `"eq"`, `"lte"`, `"gte"`).
   * @param value - The reference count to compare against.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).toolCallCount("gte", 1);
   * ```
   */
  toolCallCount(operator: string, value: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONSTRAINT, {
      field: "steps[?type=='tool_call'].length",
      operator,
      value,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Generic constraint assertion on any field.
   *
   * @param field - Dot-delimited path into the trace (e.g. `"metadata.cost_usd"`).
   * @param operator - Comparison operator.
   * @param opts.value - Reference value for single-operand operators.
   * @param opts.min - Minimum for `"between"` operator.
   * @param opts.max - Maximum for `"between"` operator.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).constraint("metadata.cost_usd", "lte", { value: 0.1 });
   * ```
   */
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

  /**
   * Assert that tools were called in the given order (non-contiguous allowed).
   *
   * @param tools - Ordered list of tool names expected.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).toolsCalledInOrder(["search", "write"]);
   * ```
   */
  toolsCalledInOrder(tools: string[], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, {
      check: "contains_in_order",
      tools,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert that tools were called in exact contiguous order.
   *
   * @param tools - Exact ordered list of tool names.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).toolsCalledExactly(["search", "summarize", "respond"]);
   * ```
   */
  toolsCalledExactly(tools: string[], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, {
      check: "exact_order",
      tools,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert a tool is not called more than `maxRepetitions` times consecutively.
   *
   * @param tool - The tool name to check.
   * @param maxRepetitions - Maximum allowed consecutive calls (default `1`).
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).noToolLoops("fetch", 2);
   * ```
   */
  noToolLoops(tool: string, maxRepetitions = 1, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, {
      check: "loop_detection",
      tool,
      max_repetitions: maxRepetitions,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert no tool is called more than once.
   *
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).noDuplicateTools();
   * ```
   */
  noDuplicateTools(opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, { check: "no_duplicates", soft: opts?.soft ?? false });
  }

  /**
   * Assert that all listed tools were called at least once.
   *
   * @param tools - Tool names that must appear in the trace.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).requiredTools(["search", "respond"]);
   * ```
   */
  requiredTools(tools: string[], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, {
      check: "required_tools",
      tools,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert that none of the listed tools were called.
   *
   * @param tools - Tool names that must not appear in the trace.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).forbiddenTools(["delete_user", "drop_table"]);
   * ```
   */
  forbiddenTools(tools: string[], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE, {
      check: "forbidden_tools",
      tools,
      soft: opts?.soft ?? false,
    });
  }

  // -- Layer 4: Content --

  /**
   * Assert the output message contains the given string.
   *
   * @param value - Substring to search for.
   * @param opts.caseSensitive - Enable case-sensitive matching (default `false`).
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).outputContains("refund approved");
   * ```
   */
  outputContains(value: string, opts?: { caseSensitive?: boolean; soft?: boolean }): this {
    return this.add(TYPE_CONTENT, {
      target: "output.message",
      check: "contains",
      value,
      case_sensitive: opts?.caseSensitive ?? false,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert the output message does not contain the given string.
   *
   * @param value - Substring that must be absent.
   * @param opts.caseSensitive - Enable case-sensitive matching (default `false`).
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).outputNotContains("internal error");
   * ```
   */
  outputNotContains(value: string, opts?: { caseSensitive?: boolean; soft?: boolean }): this {
    return this.add(TYPE_CONTENT, {
      target: "output.message",
      check: "not_contains",
      value,
      case_sensitive: opts?.caseSensitive ?? false,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert the output message matches the given regular expression.
   *
   * @param pattern - Regex pattern string.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).outputMatchesRegex("order #\\d{5}");
   * ```
   */
  outputMatchesRegex(pattern: string, opts?: { soft?: boolean }): this {
    return this.add(TYPE_CONTENT, {
      target: "output.message",
      check: "regex_match",
      value: pattern,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert the output message contains all of the given keywords.
   *
   * @param keywords - Keywords that must all be present.
   * @param opts.caseSensitive - Enable case-sensitive matching (default `false`).
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).outputHasAllKeywords(["refund", "processed"]);
   * ```
   */
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

  /**
   * Assert the output message contains at least one of the given keywords.
   *
   * @param keywords - Keywords where at least one must be present.
   * @param opts.caseSensitive - Enable case-sensitive matching (default `false`).
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).outputHasAnyKeyword(["yes", "confirmed"]);
   * ```
   */
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

  /**
   * Assert the output message contains none of the forbidden terms.
   *
   * @param terms - Terms that must not appear in the output.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).outputForbids(["password", "api_key"]);
   * ```
   */
  outputForbids(terms: string[]): this {
    return this.add(TYPE_CONTENT, {
      target: "output.message",
      check: "forbidden",
      values: terms,
    });
  }

  /**
   * Generic content-contains check on any target path.
   *
   * @param target - Dot-delimited path into the trace.
   * @param value - Substring to search for.
   * @param opts.caseSensitive - Enable case-sensitive matching (default `false`).
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).contentContains("steps[0].result.text", "success");
   * ```
   */
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

  /**
   * Assert the output is semantically similar to a reference string via embeddings.
   *
   * @param reference - The reference text to compare against.
   * @param opts.threshold - Minimum cosine similarity (default `0.8`).
   * @param opts.model - Override the embedding model.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).outputSimilarTo("The order has been refunded", { threshold: 0.85 });
   * ```
   */
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

  /**
   * Assert the output passes LLM judge evaluation against given criteria.
   *
   * @param criteria - Natural language description of what makes a good response.
   * @param opts.rubric - Rubric name (default `"default"`).
   * @param opts.threshold - Minimum passing score (default `0.8`).
   * @param opts.model - Override the judge model.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).passesJudge("Response is polite and helpful", { threshold: 0.9 });
   * ```
   */
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

  /**
   * Assert that a specific agent was called in the trace tree.
   *
   * @param agentId - The agent identifier to look for.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).agentCalled("summarizer");
   * ```
   */
  agentCalled(agentId: string, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "agent_called",
      agent_id: agentId,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert the trace tree delegation depth does not exceed `maxDepth`.
   *
   * @param maxDepth - Maximum allowed delegation depth.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).delegationDepth(3);
   * ```
   */
  delegationDepth(maxDepth: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "delegation_depth",
      max_depth: maxDepth,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert a sub-agent's output contains the given string.
   *
   * @param agentId - The agent whose output to check.
   * @param value - Substring to search for.
   * @param opts.caseSensitive - Enable case-sensitive matching (default `false`).
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).agentOutputContains("summarizer", "key findings");
   * ```
   */
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

  /**
   * Assert data flows from one agent's output to another agent's input.
   *
   * @param fromAgent - Source agent ID.
   * @param toAgent - Destination agent ID.
   * @param field - The data field that must flow between agents.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).crossAgentDataFlow("researcher", "writer", "findings");
   * ```
   */
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

  /**
   * Assert agent delegations follow the specified transitions.
   *
   * @param transitions - Array of `[fromAgent, toAgent]` pairs.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).followsTransitions([["root", "planner"], ["planner", "executor"]]);
   * ```
   */
  followsTransitions(transitions: [string, string][], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "follows_transitions",
      transitions,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert aggregate cost across the entire trace tree is at or below `maxCost`.
   *
   * @param maxCost - Maximum allowed aggregate cost in USD.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).aggregateCostUnder(0.50);
   * ```
   */
  aggregateCostUnder(maxCost: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "aggregate_cost",
      operator: "lte",
      value: maxCost,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert aggregate tokens across the entire trace tree is at or below `maxTokens`.
   *
   * @param maxTokens - Maximum allowed aggregate token count.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).aggregateTokensUnder(10000);
   * ```
   */
  aggregateTokensUnder(maxTokens: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "aggregate_tokens",
      operator: "lte",
      value: maxTokens,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert `agentA` started before `agentB` in the trace tree.
   *
   * @param agentA - The agent expected to run first.
   * @param agentB - The agent expected to run second.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).agentOrderedBefore("planner", "executor");
   * ```
   */
  agentOrderedBefore(agentA: string, agentB: string, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "agent_ordered_before",
      agent_a: agentA,
      agent_b: agentB,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert `agentA` and `agentB` ran concurrently (overlapping wall-clock time).
   *
   * @param agentA - First agent ID.
   * @param agentB - Second agent ID.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).agentsOverlap("worker-1", "worker-2");
   * ```
   */
  agentsOverlap(agentA: string, agentB: string, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "agents_overlap",
      agent_a: agentA,
      agent_b: agentB,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert a specific agent's wall-clock duration is under `maxMs` milliseconds.
   *
   * @param agentId - The agent to check.
   * @param maxMs - Maximum allowed wall-clock time in milliseconds.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).agentWallTimeUnder("summarizer", 5000);
   * ```
   */
  agentWallTimeUnder(agentId: string, maxMs: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "agent_wall_time_under",
      agent_id: agentId,
      max_ms: maxMs,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert agents ran in ordered groups (parallel within each group, sequential across groups).
   *
   * @param groups - Array of agent-ID arrays. Each sub-array is a parallel group.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).orderedAgents([["planner"], ["worker-1", "worker-2"], ["aggregator"]]);
   * ```
   */
  orderedAgents(groups: string[][], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "ordered_agents",
      groups,
      soft: opts?.soft ?? false,
    });
  }

  // -- Layer 8: Plugin --

  /**
   * Assert via a registered plugin (Layer 8).
   *
   * @param pluginId - Identifier of the plugin registered with the engine.
   * @param config - Plugin-specific configuration passed at evaluation time.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).plugin("custom-safety-check", { threshold: 0.95 });
   * ```
   */
  plugin(
    pluginId: string,
    config?: Record<string, unknown>,
    opts?: { soft?: boolean },
  ): this {
    const spec: Record<string, unknown> = { plugin_id: pluginId, soft: opts?.soft ?? false };
    if (config !== undefined) spec.config = config;
    return this.add(TYPE_PLUGIN, spec);
  }

  // -- TraceTree Analytics (P9 parity) --

  /**
   * Assert aggregate latency across the trace tree is at or below `maxMs`.
   *
   * @param maxMs - Maximum allowed aggregate latency in milliseconds.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).aggregateLatencyUnder(5000);
   * ```
   */
  aggregateLatencyUnder(maxMs: number, opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "aggregate_latency",
      operator: "lte",
      value: maxMs,
      soft: opts?.soft ?? false,
    });
  }

  /**
   * Assert that all specified tools were called across the entire trace tree.
   *
   * @param toolNames - Tool names that must appear somewhere in the tree.
   * @param opts.soft - When `true` the failure is non-blocking.
   * @returns The chain for fluent composition.
   *
   * @example
   * ```ts
   * expect(result).allToolsCalled(["search", "summarize"]);
   * ```
   */
  allToolsCalled(toolNames: string[], opts?: { soft?: boolean }): this {
    return this.add(TYPE_TRACE_TREE, {
      check: "all_tools_called",
      tools: toolNames,
      soft: opts?.soft ?? false,
    });
  }
}

/**
 * Create an assertion chain for an agent result.
 *
 * Accepts either an {@link AgentResult} or a raw {@link Trace}. When a
 * `Trace` is passed it is automatically wrapped in an `AgentResult`.
 *
 * @param result - An `AgentResult` or raw `Trace` to build assertions on.
 * @returns A new {@link ExpectChain} for fluent assertion building.
 *
 * @example
 * ```ts
 * attestExpect(result).outputContains("hello").costUnder(0.05);
 * attestExpect(trace).requiredTools(["search"]);
 * ```
 */
export function attestExpect(result: AgentResult | Trace): ExpectChain {
  if (result instanceof AgentResult) {
    return new ExpectChain(result);
  }
  // Treat as Trace — wrap in AgentResult
  return new ExpectChain(new AgentResult(result));
}
