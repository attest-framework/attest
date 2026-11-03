// Step type constants
export const STEP_LLM_CALL = "llm_call" as const;
export const STEP_TOOL_CALL = "tool_call" as const;
export const STEP_RETRIEVAL = "retrieval" as const;
export const STEP_AGENT_CALL = "agent_call" as const;

// Status constants
export const STATUS_PASS = "pass" as const;
export const STATUS_SOFT_FAIL = "soft_fail" as const;
export const STATUS_HARD_FAIL = "hard_fail" as const;
export const STATUS_NOT_EVALUATED = "not_evaluated" as const;

// Reasons carried by a not_evaluated result (AssertionResult.reason)
export const REASON_PROVIDER_UNAVAILABLE = "provider_unavailable" as const;
export const REASON_PROVIDER_NOT_CONFIGURED = "provider_not_configured" as const;
export const REASON_SIMULATION = "simulation" as const;
export const REASON_MISSING_EVIDENCE = "missing_evidence" as const;
export const REASON_SHORT_CIRCUITED = "short_circuited" as const;
export const REASON_BUDGET_EXHAUSTED = "budget_exhausted" as const;
export const REASON_JUDGE_ABSTAINED = "judge_abstained" as const;
export const REASON_REPLAY_MISS = "replay_miss" as const;

// Test outcomes derived from a batch of assertion results (AgentResult.outcome)
export const OUTCOME_PASS = "pass" as const;
export const OUTCOME_FAIL = "fail" as const;
export const OUTCOME_SKIP = "skip" as const;
export type Outcome = typeof OUTCOME_PASS | typeof OUTCOME_FAIL | typeof OUTCOME_SKIP;

// Assertion type constants
export const TYPE_SCHEMA = "schema" as const;
export const TYPE_CONSTRAINT = "constraint" as const;
export const TYPE_TRACE = "trace" as const;
export const TYPE_EFFECT = "effect" as const;
export const TYPE_CONTENT = "content" as const;
export const TYPE_EMBEDDING = "embedding" as const;
export const TYPE_LLM_JUDGE = "llm_judge" as const;
export const TYPE_TRACE_TREE = "trace_tree" as const;
export const TYPE_PLUGIN = "plugin" as const;

// Error code constants
export const ERR_INVALID_TRACE = 1001 as const;
export const ERR_ASSERTION_ERROR = 1002 as const;
export const ERR_PROVIDER_ERROR = 2001 as const;
export const ERR_ENGINE_ERROR = 3001 as const;
export const ERR_TIMEOUT = 3002 as const;
export const ERR_SESSION_ERROR = 3003 as const;
