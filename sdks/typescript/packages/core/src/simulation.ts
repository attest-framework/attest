import type { Assertion, AssertionResult, EvaluateBatchResult } from "./proto/types.js";

/**
 * Return a deterministic all-pass result for every assertion without
 * contacting the engine or any LLM provider. Used when simulation mode
 * is active (`ATTEST_SIMULATION=1` or `config({ simulation: true })`).
 */
export function simulationEvaluateBatch(
  assertions: readonly Assertion[],
): EvaluateBatchResult {
  const results: AssertionResult[] = assertions.map((assertion) => ({
    assertion_id: assertion.assertion_id,
    status: "pass",
    score: 1.0,
    explanation: `[simulation] assertion '${assertion.assertion_id}' passed (type: ${assertion.type})`,
    cost: 0,
    duration_ms: 0,
  }));

  return {
    results,
    total_cost: 0,
    total_duration_ms: 0,
  };
}
