package mcp

import "encoding/json"

// The tool table is static: tools are registered once, in this order, with
// these descriptions and schemas. Nothing about the table depends on the
// project, the environment or the call history.

const traceCheckInput = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["path"],
  "properties": {
    "path": {"type": "string", "minLength": 1, "description": "OTLP/JSON trace export or capture fixture, relative to the project root or absolute inside it."}
  }
}`

const traceCheckOutput = `{
  "type": "object",
  "required": ["ok", "schema_version", "spans", "steps", "issues", "coverage"],
  "properties": {
    "ok": {"type": "boolean"},
    "schema_version": {"type": "integer"},
    "trace_id": {"type": "string"},
    "spans": {"type": "integer"},
    "steps": {"type": "object"},
    "issues": {"type": "array", "items": {"type": "object", "required": ["severity", "code", "message"]}},
    "coverage": {"type": "array", "items": {"type": "object"}}
  }
}`

const evaluateInput = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["assertions"],
  "properties": {
    "trace_path": {"type": "string", "minLength": 1, "description": "OTLP/JSON export or capture fixture holding the trace."},
    "fixture_path": {"type": "string", "minLength": 1, "description": "Capture fixture holding the trace."},
    "trace": {"type": "object", "description": "An inline trace-v2 object."},
    "assertions": {
      "type": "array",
      "minItems": 1,
      "maxItems": 200,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["assertion_id", "type", "spec"],
        "properties": {
          "assertion_id": {"type": "string", "minLength": 1, "maxLength": 256},
          "type": {"type": "string", "enum": ["schema", "constraint", "trace", "content", "embedding", "llm_judge", "trace_tree", "effect"]},
          "spec": {"type": "object"}
        }
      }
    }
  }
}`

const evaluateOutput = `{
  "type": "object",
  "required": ["summary", "results"],
  "properties": {
    "summary": {"$ref": "#/$defs/summary"},
    "results": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["assertion_id", "status", "explanation"],
        "properties": {
          "assertion_id": {"type": "string"},
          "status": {"enum": ["pass", "soft_fail", "hard_fail", "not_evaluated"]},
          "reason": {"type": "string"},
          "explanation": {"type": "string"},
          "failure_class": {"type": "string"}
        }
      }
    }
  },
  "$defs": {"summary": ` + summaryDef + `}
}`

const summaryDef = `{
  "type": "object",
  "required": ["total", "pass", "soft_fail", "hard_fail", "not_evaluated"],
  "properties": {
    "total": {"type": "integer"},
    "pass": {"type": "integer"},
    "soft_fail": {"type": "integer"},
    "hard_fail": {"type": "integer"},
    "not_evaluated": {"type": "integer"},
    "error": {"type": "integer"}
  }
}`

const explainInput = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["report_path"],
  "properties": {
    "report_path": {"type": "string", "minLength": 1, "description": "A report-v2 JSON file."},
    "test_id": {"type": "string", "minLength": 1, "description": "Only the rows of this test."}
  }
}`

const explainOutput = `{
  "type": "object",
  "required": ["report_path", "findings"],
  "properties": {
    "report_path": {"type": "string"},
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["test_id", "assertion_id", "status", "guide", "next_step"],
        "properties": {
          "test_id": {"type": "string"},
          "assertion_id": {"type": "string"},
          "status": {"enum": ["soft_fail", "hard_fail", "not_evaluated"]},
          "reason": {"type": "string"},
          "explanation": {"type": "string"},
          "failure_class": {"type": "string"},
          "file": {"type": "string"},
          "line": {"type": "integer"},
          "guide": {"type": "string", "description": "Topic for attest guide <topic>."},
          "next_step": {"type": "string"}
        }
      }
    }
  }
}`

const draftInput = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "fixture_path": {"type": "string", "minLength": 1},
    "trace_path": {"type": "string", "minLength": 1, "description": "OTLP/JSON export or capture fixture."},
    "expect_fail": {"type": "boolean", "description": "Draft for an incident: allow one call per id, so repeated calls become the expected failures. Defaults to the fixture's own setting, else false."}
  }
}`

const draftOutput = `{
  "type": "object",
  "required": ["source", "expect_fail", "trace_check", "verified", "contracts"],
  "properties": {
    "source": {"type": "string"},
    "expect_fail": {"type": "boolean"},
    "verified": {"type": "boolean"},
    "trace_check": {"type": "object", "required": ["ok", "errors", "warnings"]},
    "contracts": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "kind", "description", "type", "spec", "status", "explanation"],
        "properties": {
          "id": {"type": "string"},
          "kind": {"type": "string"},
          "description": {"type": "string"},
          "type": {"type": "string"},
          "statement": {"type": "string", "description": "The contract as a run_contract statement."},
          "spec": {"type": "object"},
          "status": {"type": "string"},
          "explanation": {"type": "string"},
          "python": {"type": "string"},
          "typescript": {"type": "string"},
          "error": {"type": "string"}
        }
      }
    }
  }
}`

const runInput = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["statements"],
  "properties": {
    "statements": {
      "type": "array",
      "minItems": 1,
      "maxItems": 50,
      "items": {"type": "string", "maxLength": 8192}
    },
    "fixture_path": {"type": "string", "minLength": 1},
    "trace_path": {"type": "string", "minLength": 1, "description": "OTLP/JSON export or capture fixture."},
    "world_path": {"type": "string", "minLength": 1, "description": "A world-v1 snapshot or a capture fixture whose world is used. Defaults to the fixture's own world."}
  }
}`

const runOutput = `{
  "type": "object",
  "required": ["source", "summary", "results"],
  "properties": {
    "source": {"type": "string"},
    "summary": {"$ref": "#/$defs/summary"},
    "results": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["statement", "status", "explanation"],
        "properties": {
          "statement": {"type": "string"},
          "status": {"enum": ["pass", "soft_fail", "hard_fail", "not_evaluated", "error"]},
          "reason": {"type": "string"},
          "explanation": {"type": "string"},
          "failure_class": {"type": "string"},
          "spec": {"type": "object"},
          "python": {"type": "string"},
          "typescript": {"type": "string"}
        }
      }
    }
  },
  "$defs": {"summary": ` + summaryDef + `}
}`

const fixtureInfoInput = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["path"],
  "properties": {
    "path": {"type": "string", "minLength": 1, "description": "A capture fixture."}
  }
}`

const fixtureInfoOutput = `{
  "type": "object",
  "required": ["path", "name", "source", "trace", "exchanges", "contracts", "world", "verification", "redaction"],
  "properties": {
    "path": {"type": "string"},
    "name": {"type": "string"},
    "source": {"type": "object", "required": ["kind", "replay_capable"]},
    "trace": {"type": "object", "required": ["trace_id", "agent_id", "steps"]},
    "exchanges": {"type": "integer"},
    "contracts": {"type": "array", "items": {"type": "object", "required": ["id", "kind", "description", "type"]}},
    "world": {"type": "object", "required": ["present"]},
    "verification": {"type": "object", "required": ["verified", "expect_fail", "expected_failures", "trace_check"]},
    "redaction": {"type": "object", "required": ["clean", "hits"]}
  }
}`

// ToolInfo describes one tool for tools/list and `attest mcp --tools --json`.
type ToolInfo struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
}

type toolDef struct {
	ToolInfo
	run func(e *env, args json.RawMessage) (any, error)
}

const deterministicNote = " Runs the deterministic layers only (schema, constraint, trace, content, trace tree, effect). " +
	"Embedding and judge assertions return not_evaluated with reason provider_not_configured: the server makes no model calls and no network calls."

// tools is the fixed toolset, in registration order.
var tools = []toolDef{
	{
		ToolInfo: ToolInfo{
			Name: "trace_check",
			Description: "Check the structure of an OTLP/JSON trace export or a capture fixture, as `attest trace check --json` does: " +
				"span and step counts, structural issues and missing evidence. Read-only.",
			InputSchema: json.RawMessage(traceCheckInput), OutputSchema: json.RawMessage(traceCheckOutput),
		},
		run: (*env).traceCheck,
	},
	{
		ToolInfo: ToolInfo{
			Name: "evaluate",
			Description: "Evaluate assertion specs ({assertion_id, type, spec}) against a trace from trace_path, fixture_path or an inline trace-v2 object; give exactly one. " +
				"Returns one result per assertion with a status of pass, soft_fail, hard_fail or not_evaluated." + deterministicNote,
			InputSchema: json.RawMessage(evaluateInput), OutputSchema: json.RawMessage(evaluateOutput),
		},
		run: (*env).evaluate,
	},
	{
		ToolInfo: ToolInfo{
			Name: "explain_failure",
			Description: "Read a report-v2 file and explain every failing or not-evaluated assertion: status, reason, explanation, failure class, source file and line, " +
				"the `attest guide <topic>` to read next and a one-line next step. Optionally only one test.",
			InputSchema: json.RawMessage(explainInput), OutputSchema: json.RawMessage(explainOutput),
		},
		run: (*env).explainFailure,
	},
	{
		ToolInfo: ToolInfo{
			Name: "draft_contract",
			Description: "Draft deterministic behavior contracts from a captured run (fixture_path, or trace_path) and verify them on that run. " +
				"Each contract comes as a run_contract statement, its assertion spec, its verification status, and the Python and TypeScript test lines to paste.",
			InputSchema: json.RawMessage(draftInput), OutputSchema: json.RawMessage(draftOutput),
		},
		run: (*env).draftContract,
	},
	{
		ToolInfo: ToolInfo{
			Name: "run_contract",
			Description: "Evaluate contract statements against a captured run. A statement is one call in Python call syntax with literal arguments, for example " +
				`tool_cardinality("issue_refund", per="order_id", at_most=1). ` +
				"Only these methods exist: " + methodList() + ". Anything else is rejected with a position; nothing is evaluated as code. " +
				"Effect checks (count, unique, idempotent) read the world from world_path or the fixture. " +
				"Returns per statement a status (pass, soft_fail, hard_fail, not_evaluated or error), the explanation, the spec and the Python and TypeScript test lines." + deterministicNote,
			InputSchema: json.RawMessage(runInput), OutputSchema: json.RawMessage(runOutput),
		},
		run: (*env).runContract,
	},
	{
		ToolInfo: ToolInfo{
			Name: "fixture_info",
			Description: "Describe a capture fixture: name, how it was captured, trace and exchange counts, drafted contracts, world summary, " +
				"verification and a redaction check. Read-only.",
			InputSchema: json.RawMessage(fixtureInfoInput), OutputSchema: json.RawMessage(fixtureInfoOutput),
		},
		run: (*env).fixtureInfo,
	},
}

// Tools lists the fixed toolset.
func Tools() []ToolInfo {
	out := make([]ToolInfo, len(tools))
	for i, t := range tools {
		out[i] = t.ToolInfo
	}
	return out
}
