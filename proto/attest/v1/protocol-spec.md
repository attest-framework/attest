# Attest Protocol Specification — v1

**Status:** Draft
**Protocol Version:** 1
**Date:** 2026-02-18

---

## 1. Transport Layer

### 1.1 Mechanism

The Attest protocol uses **JSON-RPC 2.0 over stdio**. The SDK spawns the engine as a child subprocess and communicates exclusively over the subprocess's stdin and stdout file descriptors.

```
SDK Process
  │
  ├── stdin  ──►  Engine Process (receives requests)
  │
  └── stdout ◄──  Engine Process (sends responses + notifications)

stderr ──► Engine debug log output only (never parsed by SDK)
```

### 1.2 Message Framing

- Messages are **newline-delimited JSON** (NDJSON)
- Each message is a single JSON object on one line
- No pretty-printing on the wire — compact JSON only
- Messages are terminated by `\n` (LF, ASCII 0x0A)
- Carriage returns (`\r`) are not used

**Wire example (two messages):**
```
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"sdk_name":"attest-python","sdk_version":"0.1.0","protocol_version":1,"required_capabilities":["layers_1_4"],"preferred_encoding":"json"}}\n
{"jsonrpc":"2.0","id":1,"result":{"engine_version":"0.1.0","protocol_version":1,"capabilities":["layers_1_4"],"missing":[],"compatible":true,"encoding":"json"}}\n
```

### 1.3 Stderr Logging

The engine writes structured JSON log lines to stderr for diagnostic purposes. These are **never parsed by the SDK**. The SDK may forward stderr to its own logging infrastructure.

Engine log line format:
```json
{"level":"info","ts":"2026-02-18T10:30:00.000Z","logger":"engine","msg":"evaluation complete","trace_id":"trc_abc123","duration_ms":142}
```

Log levels controlled by `--log-level` flag on engine startup: `debug`, `info`, `warn`, `error`.

### 1.4 Lifecycle

1. SDK spawns engine subprocess with `--log-level <level>` and optional `--config <path>`
2. SDK sends `initialize` request immediately
3. Engine responds with capability negotiation result
4. SDK sends `evaluate_batch` requests as needed
5. SDK sends `shutdown` when done; engine drains and exits

### 1.5 Concurrency

- The engine processes requests concurrently
- Request IDs uniquely identify in-flight requests
- Responses may arrive out of order relative to requests
- The SDK must match responses to requests by `id`
- Maximum concurrent in-flight requests: 64 (engine-configured, advertised in initialize response)

---

## 2. Protocol Methods

### 2.1 `initialize`

Sent by the SDK immediately after spawning the engine. Establishes the session and negotiates capabilities and encoding.

#### Request

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "sdk_name": "attest-python",
    "sdk_version": "0.1.0",
    "protocol_version": 1,
    "required_capabilities": ["layers_1_4", "soft_failures"],
    "preferred_encoding": "json"
  }
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `sdk_name` | string | yes | SDK identifier, e.g. `"attest-python"`, `"attest-typescript"`, `"attest-go"` |
| `sdk_version` | string | yes | SDK semver, e.g. `"0.1.0"` |
| `protocol_version` | int | yes | Protocol version the SDK targets. Currently `1`. |
| `required_capabilities` | []string | yes | Capabilities the SDK requires to function. Engine returns `compatible: false` if any are missing. |
| `preferred_encoding` | string | yes | Always `"json"` for v1. Reserved for future binary encoding. |

#### Response

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "engine_version": "0.1.0",
    "protocol_version": 1,
    "capabilities": ["layers_1_4", "soft_failures"],
    "missing": [],
    "compatible": true,
    "encoding": "json",
    "max_concurrent_requests": 64,
    "max_trace_size_bytes": 10485760,
    "max_steps_per_trace": 10000
  }
}
```

**Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `engine_version` | string | Engine semver |
| `protocol_version` | int | Protocol version the engine is using |
| `capabilities` | []string | Full list of capabilities this engine supports |
| `missing` | []string | Required capabilities from the request not supported by this engine |
| `compatible` | bool | `false` if `missing` is non-empty. SDK should abort if `false`. |
| `encoding` | string | Negotiated encoding. Always `"json"` for v1. |
| `max_concurrent_requests` | int | Maximum simultaneous in-flight requests |
| `max_trace_size_bytes` | int | Maximum accepted trace payload size in bytes |
| `max_steps_per_trace` | int | Maximum number of steps in a single trace |

#### Capability Identifiers

Capabilities are additive. New capabilities never remove or alter old ones.

| Capability | Version | Description |
|------------|---------|-------------|
| `layers_1_4` | v0.1+ | Deterministic assertion layers: schema, constraint, trace, content |
| `layers_5_6` | v0.2+ | Embedding similarity (layer 5) and LLM-as-judge (layer 6) |
| `soft_failures` | v0.2+ | Soft failure classification and per-test-suite budgets |
| `simulation` | v0.3+ | Simulated users, mock tools, fault injection |
| `multi_agent` | v0.3+ | Hierarchical trace trees, cross-agent assertions |
| `continuous_eval` | v0.4+ | Production trace ingestion and sampling pipelines |
| `plugins` | v0.4+ | Custom assertions and judges via plugin interface |

#### Error: Incompatible Protocol Version

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": 3003,
    "message": "protocol version 3 not supported; engine supports versions 1 and 2",
    "data": {
      "error_type": "SESSION_ERROR",
      "retryable": false,
      "detail": "Upgrade the engine binary or downgrade the SDK protocol_version"
    }
  }
}
```

---

### 2.2 `evaluate_batch`

**Primary evaluation path.** The SDK collects all assertions for a trace and submits them in a single request. This minimizes subprocess communication overhead and allows the engine to optimize evaluation order (e.g., run cheap layers first and short-circuit on hard failures).

#### Request

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "evaluate_batch",
  "params": {
    "trace": {
      "schema_version": 1,
      "trace_id": "trc_abc123",
      "agent_id": "customer-service-agent",
      "input": {
        "user_message": "I want a refund for order ORD-123",
        "context": {}
      },
      "steps": [
        {
          "type": "llm_call",
          "name": "reasoning",
          "args": {
            "model": "gpt-4.1",
            "prompt": "You are a customer service agent. User: I want a refund for order ORD-123"
          },
          "result": {
            "completion": "I need to look up the order to verify eligibility."
          },
          "metadata": { "duration_ms": 1200, "cost_usd": 0.003 }
        },
        {
          "type": "tool_call",
          "name": "lookup_order",
          "args": { "order_id": "ORD-123" },
          "result": { "status": "delivered", "amount": 89.99, "eligible_for_refund": true },
          "metadata": { "duration_ms": 45 }
        },
        {
          "type": "tool_call",
          "name": "process_refund",
          "args": { "order_id": "ORD-123", "amount": 89.99 },
          "result": { "refund_id": "RFD-001", "estimated_days": 3 },
          "metadata": { "duration_ms": 120 }
        }
      ],
      "output": {
        "message": "Your refund of $89.99 has been processed. You'll see it in 3 business days. Refund ID: RFD-001.",
        "structured": { "refund_id": "RFD-001", "confidence": 0.95 }
      },
      "metadata": {
        "total_tokens": 1350,
        "cost_usd": 0.0067,
        "latency_ms": 4200,
        "model": "gpt-4.1",
        "timestamp": "2026-02-18T10:30:00Z"
      },
      "parent_trace_id": null
    },
    "assertions": [
      {
        "assertion_id": "assert_001",
        "type": "schema",
        "request_id": "req_idempotency_key_001",
        "spec": {
          "target": "steps[?name=='lookup_order'].result",
          "schema": {
            "$schema": "https://json-schema.org/draft/2020-12",
            "type": "object",
            "required": ["status", "amount"],
            "properties": {
              "status": { "type": "string" },
              "amount": { "type": "number", "minimum": 0 }
            }
          }
        }
      },
      {
        "assertion_id": "assert_002",
        "type": "constraint",
        "request_id": "req_idempotency_key_002",
        "spec": {
          "field": "metadata.cost_usd",
          "operator": "lte",
          "value": 0.01
        }
      },
      {
        "assertion_id": "assert_003",
        "type": "trace",
        "request_id": "req_idempotency_key_003",
        "spec": {
          "check": "contains_in_order",
          "tools": ["lookup_order", "process_refund"]
        }
      },
      {
        "assertion_id": "assert_004",
        "type": "content",
        "request_id": "req_idempotency_key_004",
        "spec": {
          "target": "output.message",
          "check": "contains",
          "value": "refund"
        }
      },
      {
        "assertion_id": "assert_005",
        "type": "content",
        "request_id": "req_idempotency_key_005",
        "spec": {
          "target": "output.message",
          "check": "not_contains",
          "value": "cannot process",
          "soft": true
        }
      },
      {
        "assertion_id": "assert_006",
        "type": "llm_judge",
        "request_id": "req_idempotency_key_006",
        "spec": {
          "rubric": "helpfulness",
          "threshold": 0.7,
          "model": "gpt-4.1"
        }
      }
    ]
  }
}
```

**Assertion object fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `assertion_id` | string | yes | Unique identifier within this batch. Echoed in results. |
| `type` | string | yes | Assertion layer type. One of: `schema`, `constraint`, `trace`, `content`, `embedding`, `llm_judge` |
| `spec` | object | yes | Type-specific assertion parameters. See Section 4. |
| `request_id` | string | no | Idempotency key. If the same `request_id` is submitted twice, the engine returns the cached result. |

#### Response

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "results": [
      {
        "assertion_id": "assert_001",
        "status": "pass",
        "score": 1.0,
        "explanation": "Tool result for 'lookup_order' matches schema: all required fields present, types valid.",
        "cost": 0.0,
        "duration_ms": 2,
        "request_id": "req_idempotency_key_001"
      },
      {
        "assertion_id": "assert_002",
        "status": "pass",
        "score": 1.0,
        "explanation": "metadata.cost_usd = 0.0067, constraint lte 0.01 satisfied.",
        "cost": 0.0,
        "duration_ms": 1,
        "request_id": "req_idempotency_key_002"
      },
      {
        "assertion_id": "assert_003",
        "status": "pass",
        "score": 1.0,
        "explanation": "Tool sequence ['lookup_order', 'process_refund'] found in order at steps [1, 2].",
        "cost": 0.0,
        "duration_ms": 1,
        "request_id": "req_idempotency_key_003"
      },
      {
        "assertion_id": "assert_004",
        "status": "pass",
        "score": 1.0,
        "explanation": "output.message contains 'refund'.",
        "cost": 0.0,
        "duration_ms": 0,
        "request_id": "req_idempotency_key_004"
      },
      {
        "assertion_id": "assert_005",
        "status": "pass",
        "score": 1.0,
        "explanation": "output.message does not contain 'cannot process'.",
        "cost": 0.0,
        "duration_ms": 0,
        "request_id": "req_idempotency_key_005"
      },
      {
        "assertion_id": "assert_006",
        "status": "pass",
        "score": 0.91,
        "explanation": "Agent provided a clear, complete refund confirmation with reference ID and timeline. Response is on-topic and actionable.",
        "cost": 0.0012,
        "duration_ms": 1840,
        "request_id": "req_idempotency_key_006"
      }
    ],
    "total_cost": 0.0012,
    "total_duration_ms": 1845
  }
}
```

**Result object fields:**

| Field | Type | Description |
|-------|------|-------------|
| `assertion_id` | string | Matches the assertion from the request |
| `status` | string | `pass`, `soft_fail`, or `hard_fail` |
| `score` | float | 0.0 to 1.0. For boolean checks: 0.0 or 1.0. For scored checks: continuous value. |
| `explanation` | string | Human-readable explanation of the result, including relevant values |
| `cost` | float | USD cost for this assertion (non-zero for LLM-backed assertions) |
| `duration_ms` | int | Wall-clock time to evaluate this assertion |
| `request_id` | string | Echoed from the request if provided |

---

### 2.3 `shutdown`

Signals the engine to drain in-flight evaluations, flush caches, and exit cleanly. The SDK should call this before the process exits to ensure no data loss.

The engine drains in-flight evaluations for up to 30 seconds, then exits regardless.

#### Request

```json
{
  "jsonrpc": "2.0",
  "id": 99,
  "method": "shutdown",
  "params": {}
}
```

#### Response

```json
{
  "jsonrpc": "2.0",
  "id": 99,
  "result": {
    "sessions_completed": 1,
    "assertions_evaluated": 42
  }
}
```

**Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `sessions_completed` | int | Number of `initialize` sessions handled in this process lifetime |
| `assertions_evaluated` | int | Total assertions evaluated across all batches in this session |

---

### 2.4 `submit_plugin_result`

Enables SDK-side custom assertions. The SDK executes custom assertion logic in its own language runtime (Python, TypeScript, Go) and submits the pre-computed result to the engine for recording, aggregation, and reporting.

This design avoids embedding a Python or JavaScript runtime inside the Go engine binary. The engine treats plugin results identically to natively evaluated assertions for reporting purposes.

#### Request

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "method": "submit_plugin_result",
  "params": {
    "trace_id": "trc_abc123",
    "plugin_name": "my_custom_safety_checker",
    "assertion_id": "plugin_assert_001",
    "result": {
      "status": "pass",
      "score": 0.88,
      "explanation": "Custom safety classifier returned score 0.88; above threshold 0.75.",
      "metadata": {
        "classifier_version": "2.1.0",
        "categories_checked": ["hate_speech", "violence", "pii"]
      }
    }
  }
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `trace_id` | string | yes | The trace this plugin result belongs to |
| `plugin_name` | string | yes | Identifier for the plugin. Must be unique within the test suite. |
| `assertion_id` | string | yes | Unique assertion identifier. Referenced in final reports. |
| `result.status` | string | yes | `pass`, `soft_fail`, or `hard_fail` |
| `result.score` | float | yes | 0.0 to 1.0 |
| `result.explanation` | string | yes | Human-readable explanation |
| `result.metadata` | object | no | Arbitrary plugin-specific metadata for debugging |

#### Response

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "result": {
    "accepted": true
  }
}
```

---

## 3. Trace Data Model

The canonical trace format represents a single agent execution from input to output, including all intermediate steps.

### 3.1 Full Trace Schema

```json
{
  "schema_version": 1,
  "trace_id": "trc_abc123",
  "agent_id": "customer-service-agent",
  "input": {
    "user_message": "I want a refund for order ORD-123",
    "context": {}
  },
  "steps": [
    {
      "type": "llm_call",
      "name": "reasoning",
      "args": {
        "model": "gpt-4.1",
        "prompt": "You are a customer service agent. User: I want a refund for order ORD-123"
      },
      "result": {
        "completion": "I need to look up the order to verify eligibility.",
        "tokens": 450
      },
      "metadata": { "duration_ms": 1200, "cost_usd": 0.003 }
    },
    {
      "type": "tool_call",
      "name": "lookup_order",
      "args": { "order_id": "ORD-123" },
      "result": { "status": "delivered", "amount": 89.99 },
      "metadata": { "duration_ms": 45 }
    },
    {
      "type": "tool_call",
      "name": "process_refund",
      "args": { "order_id": "ORD-123", "amount": 89.99 },
      "result": { "refund_id": "RFD-001" },
      "metadata": { "duration_ms": 120 }
    }
  ],
  "output": {
    "message": "Your refund of $89.99 has been processed.",
    "structured": { "refund_id": "RFD-001", "confidence": 0.95 }
  },
  "metadata": {
    "total_tokens": 1350,
    "cost_usd": 0.0067,
    "latency_ms": 4200,
    "model": "gpt-4.1",
    "timestamp": "2026-02-18T10:30:00Z"
  },
  "parent_trace_id": null
}
```

### 3.2 Top-Level Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `schema_version` | int | yes | Trace schema version. Engine supports current and previous 2 versions. |
| `trace_id` | string | yes | Globally unique trace identifier. Recommended prefix: `trc_`. |
| `agent_id` | string | no | Identifier for the agent that produced this trace. |
| `input` | object | no | Agent input. Shape is agent-defined. |
| `steps` | []Step | no | Ordered list of execution steps. May be empty for simple agents. |
| `output` | object | yes | Agent output. Must contain at least one field. |
| `metadata` | object | no | Trace-level metadata: tokens, cost, latency, model, timestamp. |
| `parent_trace_id` | string \| null | no | Set when this trace is a sub-agent invocation from a parent trace. |

### 3.3 Step Types

#### `llm_call`

An LLM completion call.

```json
{
  "type": "llm_call",
  "name": "summarize",
  "args": {
    "model": "gpt-4.1",
    "prompt": "Summarize the following order history: ...",
    "temperature": 0.0,
    "max_tokens": 512
  },
  "result": {
    "completion": "The customer has 3 orders totaling $234.50.",
    "tokens": 380,
    "finish_reason": "stop"
  },
  "metadata": { "duration_ms": 980, "cost_usd": 0.0024 }
}
```

#### `tool_call`

A tool or function call.

```json
{
  "type": "tool_call",
  "name": "search_knowledge_base",
  "args": {
    "query": "refund policy for delivered orders",
    "top_k": 3
  },
  "result": {
    "documents": [
      { "id": "policy-001", "text": "Orders can be refunded within 30 days of delivery.", "score": 0.94 }
    ]
  },
  "metadata": { "duration_ms": 62 }
}
```

#### `retrieval`

A retrieval step (RAG lookup). Semantically similar to `tool_call` but typed for assertion targeting.

```json
{
  "type": "retrieval",
  "name": "vector_search",
  "args": {
    "query_embedding": "[omitted for brevity]",
    "collection": "product_catalog",
    "top_k": 5
  },
  "result": {
    "documents": [
      { "id": "prod-456", "text": "Widget Pro — $49.99", "score": 0.91 }
    ]
  },
  "metadata": { "duration_ms": 18 }
}
```

#### `agent_call`

A sub-agent delegation. Contains a nested sub-trace for multi-agent evaluation.

```json
{
  "type": "agent_call",
  "name": "escalation_agent",
  "args": {
    "reason": "Customer requesting manager escalation",
    "context": { "order_id": "ORD-123" }
  },
  "result": {
    "resolution": "Approved full refund as goodwill gesture",
    "escalation_id": "ESC-789"
  },
  "sub_trace": {
    "schema_version": 1,
    "trace_id": "trc_sub_def456",
    "agent_id": "escalation-agent",
    "parent_trace_id": "trc_abc123",
    "steps": [],
    "output": { "resolution": "Approved full refund as goodwill gesture" },
    "metadata": { "latency_ms": 2100 }
  },
  "metadata": { "duration_ms": 2150 }
}
```

### 3.4 Step Common Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | Step type: `llm_call`, `tool_call`, `retrieval`, `agent_call` |
| `name` | string | yes | Step name, used in trace assertions (`contains_in_order`, etc.) |
| `args` | object | no | Inputs to the step |
| `result` | object | no | Output from the step |
| `sub_trace` | Trace | no | Only for `agent_call`. Nested trace of the sub-agent. |
| `metadata` | object | no | Step-level timing and cost |

---

## 4. Assertion Layers (1–6)

Each layer builds on the previous in terms of computational cost and evaluation depth. Layers 1–4 are deterministic and free. Layer 5 requires an embedding API call. Layer 6 requires an LLM API call.

The engine evaluates assertions in layer order within a batch and can short-circuit: if a `hard_fail` is detected at an early layer, later layers may be skipped (engine decides based on configuration).

---

### Layer 1 — Schema Validation

Validates tool call args, tool call results, and structured output against JSON Schema Draft 2020-12.

**Assertion type:** `schema`

**Spec fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `target` | string | yes | JSONPath expression targeting the value to validate. Supported: `output`, `output.structured`, `steps[?name=='<name>'].args`, `steps[?name=='<name>'].result` |
| `schema` | object | yes | JSON Schema Draft 2020-12 document |

**Examples:**

Validate structured output:
```json
{
  "assertion_id": "schema_output",
  "type": "schema",
  "spec": {
    "target": "output.structured",
    "schema": {
      "$schema": "https://json-schema.org/draft/2020-12",
      "type": "object",
      "required": ["refund_id", "confidence"],
      "properties": {
        "refund_id": { "type": "string", "pattern": "^RFD-\\d+$" },
        "confidence": { "type": "number", "minimum": 0.0, "maximum": 1.0 }
      },
      "additionalProperties": false
    }
  }
}
```

Validate tool call arguments:
```json
{
  "assertion_id": "schema_tool_args",
  "type": "schema",
  "spec": {
    "target": "steps[?name=='process_refund'].args",
    "schema": {
      "$schema": "https://json-schema.org/draft/2020-12",
      "type": "object",
      "required": ["order_id", "amount"],
      "properties": {
        "order_id": { "type": "string" },
        "amount": { "type": "number", "exclusiveMinimum": 0 }
      }
    }
  }
}
```

**Pass result:**
```json
{
  "assertion_id": "schema_output",
  "status": "pass",
  "score": 1.0,
  "explanation": "output.structured matches schema: all required fields present, types valid, pattern 'RFD-001' matches '^RFD-\\d+$'.",
  "cost": 0.0,
  "duration_ms": 3
}
```

**Fail result:**
```json
{
  "assertion_id": "schema_output",
  "status": "hard_fail",
  "score": 0.0,
  "explanation": "output.structured failed schema validation: confidence = 1.23 exceeds maximum 1.0.",
  "cost": 0.0,
  "duration_ms": 2
}
```

---

### Layer 2 — Constraint Checks

Numeric comparisons on trace metadata fields. Used to enforce cost, token, latency, and step count budgets.

**Assertion type:** `constraint`

**Spec fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `field` | string | yes | Dot-path into the trace. Supported: `metadata.cost_usd`, `metadata.total_tokens`, `metadata.latency_ms`, `steps.length` (count of all steps), `steps[?type=='tool_call'].length` (count of tool calls) |
| `operator` | string | yes | One of: `lt`, `lte`, `gt`, `gte`, `eq`, `between` |
| `value` | number | yes (except `between`) | Right-hand side of the comparison |
| `min` | number | yes (if `between`) | Lower bound (inclusive) |
| `max` | number | yes (if `between`) | Upper bound (inclusive) |
| `soft` | bool | no | If `true`, a failing constraint is `soft_fail` instead of `hard_fail`. Default: `false` |

**Examples:**

Enforce cost budget:
```json
{
  "assertion_id": "cost_budget",
  "type": "constraint",
  "spec": {
    "field": "metadata.cost_usd",
    "operator": "lte",
    "value": 0.01
  }
}
```

Enforce latency SLA (soft failure):
```json
{
  "assertion_id": "latency_sla",
  "type": "constraint",
  "spec": {
    "field": "metadata.latency_ms",
    "operator": "lte",
    "value": 5000,
    "soft": true
  }
}
```

Enforce token range:
```json
{
  "assertion_id": "token_range",
  "type": "constraint",
  "spec": {
    "field": "metadata.total_tokens",
    "operator": "between",
    "min": 100,
    "max": 2000
  }
}
```

**Operators:**

| Operator | Meaning |
|----------|---------|
| `lt` | field < value |
| `lte` | field ≤ value |
| `gt` | field > value |
| `gte` | field ≥ value |
| `eq` | field == value |
| `between` | min ≤ field ≤ max |

---

### Layer 3 — Trace Inspection

Structural checks on the sequence and properties of steps in the trace. Detects incorrect tool ordering, loops, and missing or duplicate steps.

**Assertion type:** `trace`

**Spec fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `check` | string | yes | Check type. See below. |
| `tools` | []string | depends | Tool names for ordering checks |
| `tool` | string | depends | Tool name for per-tool checks |
| `max_repetitions` | int | depends | Maximum times a step may repeat |
| `transitions` | []Transition | depends | Expected state machine transitions |
| `soft` | bool | no | If `true`, failure is `soft_fail`. Default: `false`. |

**Check types:**

| Check | Description | Required fields |
|-------|-------------|-----------------|
| `contains_in_order` | The specified tools appear in the trace in the given order (non-contiguous OK) | `tools` |
| `exact_order` | The specified tools appear in the trace in exactly this order with no other tool calls in between | `tools` |
| `loop_detection` | A tool is not called more than `max_repetitions` times | `tool`, `max_repetitions` |
| `no_duplicates` | No tool is called more than once | none |
| `required_tools` | All listed tools were called at least once | `tools` |
| `forbidden_tools` | None of the listed tools were called | `tools` |

**Examples:**

Require lookup before refund:
```json
{
  "assertion_id": "tool_order",
  "type": "trace",
  "spec": {
    "check": "contains_in_order",
    "tools": ["lookup_order", "process_refund"]
  }
}
```

Detect excessive API retries:
```json
{
  "assertion_id": "no_loops",
  "type": "trace",
  "spec": {
    "check": "loop_detection",
    "tool": "lookup_order",
    "max_repetitions": 2
  }
}
```

No duplicate state changes:
```json
{
  "assertion_id": "no_dupe_refunds",
  "type": "trace",
  "spec": {
    "check": "no_duplicates"
  }
}
```

---

### Layer 4 — Content Matching

Text-based checks on agent output or step results. Supports substring matching, regex patterns, and keyword sets.

**Assertion type:** `content`

**Spec fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `target` | string | yes | JSONPath to the text value. Supported: `output.message`, `output.structured.<field>`, `steps[?name=='<name>'].result.<field>` |
| `check` | string | yes | Check type. See below. |
| `value` | string | depends | For `contains`, `not_contains`, `regex_match` |
| `values` | []string | depends | For `keyword_all`, `keyword_any`, `forbidden` |
| `soft` | bool | no | If `true`, failure is `soft_fail`. Default: `false`. |
| `case_sensitive` | bool | no | For `contains`, `not_contains`, `keyword_all`, `keyword_any`. Default: `false`. |

**Check types:**

| Check | Description |
|-------|-------------|
| `contains` | Target contains `value` |
| `not_contains` | Target does not contain `value` |
| `regex_match` | Target matches regex `value` (RE2 syntax) |
| `keyword_all` | Target contains all strings in `values` |
| `keyword_any` | Target contains at least one string in `values` |
| `forbidden` | Target contains none of the strings in `values` (hard fail on any match) |

**Examples:**

Confirm refund mentioned:
```json
{
  "assertion_id": "mentions_refund",
  "type": "content",
  "spec": {
    "target": "output.message",
    "check": "contains",
    "value": "refund",
    "case_sensitive": false
  }
}
```

Refuse harmful content:
```json
{
  "assertion_id": "no_harmful_content",
  "type": "content",
  "spec": {
    "target": "output.message",
    "check": "forbidden",
    "values": ["kill", "harm", "illegal", "bomb"]
  }
}
```

Match refund ID pattern:
```json
{
  "assertion_id": "refund_id_format",
  "type": "content",
  "spec": {
    "target": "output.message",
    "check": "regex_match",
    "value": "RFD-\\d{3,}"
  }
}
```

All key terms present:
```json
{
  "assertion_id": "complete_response",
  "type": "content",
  "spec": {
    "target": "output.message",
    "check": "keyword_all",
    "values": ["refund", "business days", "RFD-"],
    "soft": true
  }
}
```

---

### Layer 5 — Embedding Similarity

Computes semantic similarity between agent output and a reference text using embedding vectors. Returns a continuous score; fails when below threshold.

**Requires capability:** `layers_5_6`

**Assertion type:** `embedding`

**Spec fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `target` | string | yes | JSONPath to text to evaluate |
| `reference` | string | yes | Reference text to compare against |
| `threshold` | float | no | Minimum cosine similarity score to pass. Default: `0.8`. Range: 0.0–1.0. |
| `model` | string | no | Embedding model to use. Default from engine config. |
| `soft` | bool | no | If `true`, failure below threshold is `soft_fail`. Default: `false`. |

**Example:**

```json
{
  "assertion_id": "semantic_similarity",
  "type": "embedding",
  "spec": {
    "target": "output.message",
    "reference": "The refund has been processed and will appear within 3 business days.",
    "threshold": 0.82
  }
}
```

**Pass result:**
```json
{
  "assertion_id": "semantic_similarity",
  "status": "pass",
  "score": 0.91,
  "explanation": "Cosine similarity 0.91 between output.message and reference text exceeds threshold 0.82.",
  "cost": 0.00004,
  "duration_ms": 210
}
```

**Fail result:**
```json
{
  "assertion_id": "semantic_similarity",
  "status": "hard_fail",
  "score": 0.61,
  "explanation": "Cosine similarity 0.61 between output.message and reference text is below threshold 0.82. The output discusses compensation rather than a direct refund.",
  "cost": 0.00004,
  "duration_ms": 205
}
```

---

### Layer 6 — LLM-as-Judge

Uses a configurable LLM to evaluate agent output against a rubric. Supports built-in rubrics (faithfulness, helpfulness, safety) and custom judge prompts.

**Requires capability:** `layers_5_6`

**Assertion type:** `llm_judge`

**Spec fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `rubric` | string | yes (if no `prompt`) | Built-in rubric: `faithfulness`, `helpfulness`, `safety`, `conciseness`, `tone` |
| `prompt` | string | yes (if no `rubric`) | Custom judge prompt. Use `{{output}}` and `{{input}}` as placeholders. |
| `threshold` | float | no | Minimum score to pass. Default: `0.7`. |
| `model` | string | no | LLM model to use as judge. Default from engine config. |
| `soft` | bool | no | If `true`, failure is `soft_fail`. Default: `false`. |
| `target` | string | no | JSONPath to text to evaluate. Default: `output.message`. |

**Built-in rubrics:**

| Rubric | Description |
|--------|-------------|
| `faithfulness` | Does the output accurately reflect information from the retrieved context? No hallucinations. |
| `helpfulness` | Does the output directly address the user's request in a useful, actionable way? |
| `safety` | Does the output avoid harmful, offensive, or dangerous content? |
| `conciseness` | Is the output appropriately brief without omitting critical information? |
| `tone` | Is the tone professional, polite, and appropriate for the context? |

**Examples:**

Built-in rubric:
```json
{
  "assertion_id": "helpfulness_check",
  "type": "llm_judge",
  "spec": {
    "rubric": "helpfulness",
    "threshold": 0.75,
    "model": "gpt-4.1"
  }
}
```

Custom judge prompt:
```json
{
  "assertion_id": "refund_policy_compliance",
  "type": "llm_judge",
  "spec": {
    "prompt": "You are evaluating a customer service agent response. The agent was asked: {{input}}. The agent responded: {{output}}. Score from 0.0 to 1.0: Does the response comply with a standard 30-day refund policy and accurately state the timeline? Return JSON: {\"score\": <float>, \"reason\": <string>}",
    "threshold": 0.8,
    "model": "gpt-4.1"
  }
}
```

**Pass result:**
```json
{
  "assertion_id": "helpfulness_check",
  "status": "pass",
  "score": 0.91,
  "explanation": "Response directly addresses refund request, provides confirmation with ID and timeline. Score 0.91 exceeds threshold 0.75.",
  "cost": 0.0018,
  "duration_ms": 2240
}
```

---

## 5. Error Codes

All errors follow the JSON-RPC 2.0 error format with an extended `data` field for structured error information.

### Error Code Table

| Code | Name | Description | Retryable |
|------|------|-------------|-----------|
| 1001 | `INVALID_TRACE` | Malformed trace: missing required fields, exceeds size limit, exceeds step limit, unsupported schema_version | No |
| 1002 | `ASSERTION_ERROR` | Assertion execution failed: invalid regex, malformed JSON Schema, unsupported JSONPath expression, unknown assertion type | No |
| 2001 | `PROVIDER_ERROR` | LLM or embedding API call failed: HTTP 429 (rate limit), HTTP 500, timeout, invalid API key | Yes |
| 3001 | `ENGINE_ERROR` | Internal engine fault: recovered panic, out of memory, unexpected nil pointer | No |
| 3002 | `TIMEOUT` | Evaluation exceeded the configured time limit | Yes |
| 3003 | `SESSION_ERROR` | Invalid session state: `evaluate_batch` called before `initialize`, `initialize` called twice, unknown method | No |

### Error Response Format

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "error": {
    "code": 1001,
    "message": "trace exceeds max size: 12000000 > 10485760 bytes",
    "data": {
      "error_type": "INVALID_TRACE",
      "retryable": false,
      "detail": "Reduce trace size by filtering steps or truncating tool results. Max allowed: 10485760 bytes (10 MB)."
    }
  }
}
```

**`data` fields:**

| Field | Type | Description |
|-------|------|-------------|
| `error_type` | string | Named error type from the table above |
| `retryable` | bool | Whether the SDK should retry this request. Provider errors are retryable; malformed requests are not. |
| `detail` | string | Actionable guidance for the developer |

### Error Examples

**INVALID_TRACE — missing required field:**
```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "error": {
    "code": 1001,
    "message": "trace missing required field: trace_id",
    "data": {
      "error_type": "INVALID_TRACE",
      "retryable": false,
      "detail": "Every trace must include a non-empty trace_id string."
    }
  }
}
```

**ASSERTION_ERROR — invalid regex:**
```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "error": {
    "code": 1002,
    "message": "assertion 'assert_007' failed: invalid regex '[unclosed'",
    "data": {
      "error_type": "ASSERTION_ERROR",
      "retryable": false,
      "detail": "The regex pattern '[unclosed' is not valid RE2 syntax. Fix the regex in assertion spec."
    }
  }
}
```

**PROVIDER_ERROR — rate limit:**
```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "error": {
    "code": 2001,
    "message": "embedding provider returned HTTP 429: rate limit exceeded",
    "data": {
      "error_type": "PROVIDER_ERROR",
      "retryable": true,
      "detail": "Wait and retry. The engine applies exponential backoff internally up to 3 attempts before surfacing this error."
    }
  }
}
```

**SESSION_ERROR — evaluate before initialize:**
```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "error": {
    "code": 3003,
    "message": "evaluate_batch called before initialize",
    "data": {
      "error_type": "SESSION_ERROR",
      "retryable": false,
      "detail": "Call initialize first to establish a session before sending evaluate_batch requests."
    }
  }
}
```

**TIMEOUT — evaluation exceeded time limit:**
```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "error": {
    "code": 3002,
    "message": "evaluation timed out after 30000ms",
    "data": {
      "error_type": "TIMEOUT",
      "retryable": true,
      "detail": "LLM-backed assertions (layers 5-6) can be slow. Reduce batch size or increase the engine timeout via --eval-timeout flag."
    }
  }
}
```

---

## 6. Forward Compatibility Rules

The protocol is designed to evolve without breaking existing SDKs or engine binaries.

### Non-Breaking Changes

The following changes are always safe and do not require a `protocol_version` bump:

1. **New fields in existing messages** — Engine and SDK implementations MUST ignore unknown fields in received messages.
2. **New methods** — Unknown methods return JSON-RPC `method_not_found` (-32601). SDKs can detect supported methods via capability negotiation.
3. **New capability identifiers** — Additive. Old SDKs ignore unknown capabilities; new SDKs declare required ones.
4. **New assertion types** — Old engines return `ASSERTION_ERROR` with `"unknown assertion type"`. SDKs should gate new assertion types on capability checks.
5. **New error codes** — SDKs must handle unknown error codes gracefully (treat as non-retryable `ENGINE_ERROR`).
6. **New step types in traces** — Engine evaluators treat unknown step types as opaque and skip type-specific processing.

### Breaking Changes (Require `protocol_version` Bump)

The following changes break compatibility and increment `protocol_version`:

1. Removing an existing field from a message
2. Changing the type of an existing field
3. Renaming an existing field
4. Changing the semantic meaning of an existing field
5. Removing a method
6. Changing the shape of error codes

### Backward Compatibility Window

- The engine supports the **current** `protocol_version` and **current - 1**.
- Example: engine at `protocol_version: 2` accepts requests from SDKs at `protocol_version: 1` or `protocol_version: 2`.
- SDKs using `protocol_version: 0` against a v2 engine receive a `SESSION_ERROR` with a migration message.

### SDK Implementation Requirements

```
MUST:
  - Ignore unknown fields in all engine responses
  - Handle unknown error codes without crashing
  - Check `compatible` field in initialize response before sending evaluate_batch

MUST NOT:
  - Fail on unexpected top-level keys in JSON responses
  - Assume exhaustive capability list is fixed
  - Depend on response field ordering
```

### Engine Implementation Requirements

```
MUST:
  - Ignore unknown fields in all SDK requests
  - Accept unknown assertion types and return ASSERTION_ERROR (not crash)
  - Return method_not_found for unknown methods
  - Honor protocol_version compatibility window (current and current - 1)

MUST NOT:
  - Fail to parse requests that have additional unknown fields
  - Change the meaning of existing fields without a protocol_version bump
```

---

## 7. Trace Validation Rules

The engine validates all traces before evaluation. Invalid traces return an `INVALID_TRACE` error immediately, before any assertions are evaluated.

### Size and Count Limits

| Rule | Limit | Error |
|------|-------|-------|
| Max trace size | 10 MB (10,485,760 bytes) | `trace exceeds max size: <actual> > 10485760 bytes` |
| Max steps per trace | 10,000 | `trace exceeds max steps: <actual> > 10000` |
| Max output length | 500,000 characters | `output.message length <actual> exceeds 500000 characters` |
| Max step payload | 1 MB per step result | `step '<name>' result exceeds 1048576 bytes` |
| Max sub-trace depth | 5 levels | `trace nesting depth <actual> exceeds maximum 5` |

### Required Fields

| Field | Validation |
|-------|------------|
| `trace_id` | Non-empty string. No whitespace-only values. |
| `output` | Non-null object with at least one field. |
| `schema_version` | Integer. Must be a supported version (current or previous 2). |

### Field Validation

| Field | Validation |
|-------|------------|
| `steps[*].type` | Must be `llm_call`, `tool_call`, `retrieval`, or `agent_call`. Unknown types are rejected in strict mode; tolerated in lax mode. |
| `steps[*].name` | Non-empty string. |
| `metadata.timestamp` | If present, must be RFC 3339 format. |
| `parent_trace_id` | If present, must be a non-empty string or explicit `null`. |

### Schema Version Policy

| schema_version | Status |
|----------------|--------|
| Current (1) | Fully supported |
| Previous (0) | Supported with deprecation warning in engine logs |
| Older | Rejected with `INVALID_TRACE` and migration message |

### Actionable Error Messages

Every `INVALID_TRACE` error includes a `detail` field with actionable guidance:

```json
{
  "code": 1001,
  "message": "trace step 'process_refund' result exceeds 1048576 bytes (actual: 1200000 bytes)",
  "data": {
    "error_type": "INVALID_TRACE",
    "retryable": false,
    "detail": "Truncate the 'result' field for step 'process_refund' before submitting. Consider filtering large arrays or binary data from tool results."
  }
}
```

### Validation Order

The engine validates in this order and stops at the first failure:

1. Top-level JSON parse
2. `schema_version` check
3. Required fields (`trace_id`, `output`)
4. Size and count limits
5. Field type and format validation
6. Step-level validation (type, name, payload size)
7. Sub-trace depth check

This order ensures the most actionable error is surfaced first.

---

*End of Attest Protocol Specification v1*
