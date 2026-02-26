package trace

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/attest-ai/attest/engine/pkg/types"
)

const (
	MaxTraceSize         = 10485760 // 10 MB
	MaxStepsPerTrace     = 10000
	MaxOutputLength      = 500000
	MaxStepPayload       = 1048576 // 1 MB
	MaxSubTraceDepth     = 5
	CurrentSchemaVersion = 1
	MinSchemaVersion     = 0
)

var validStepTypes = map[string]struct{}{
	types.StepTypeLLMCall:   {},
	types.StepTypeToolCall:  {},
	types.StepTypeRetrieval: {},
	types.StepTypeAgentCall: {},
}

// Validate validates a trace per the protocol spec section 7.
// Returns nil if the trace is valid, or an RPCError describing the first failure.
func Validate(t *types.Trace) *types.RPCError {
	return validateAtDepth(t, 0)
}

func validateAtDepth(t *types.Trace, depth int) *types.RPCError {
	// 1. schema_version check
	if t.SchemaVersion < MinSchemaVersion || t.SchemaVersion > CurrentSchemaVersion {
		return types.NewRPCError(
			types.ErrInvalidTrace,
			fmt.Sprintf("unsupported schema_version %d; engine supports versions %d to %d", t.SchemaVersion, MinSchemaVersion, CurrentSchemaVersion),
			types.ErrTypeInvalidTrace,
			false,
			fmt.Sprintf("Set schema_version to %d (current) or %d (previous, deprecated). Version %d is not supported.", CurrentSchemaVersion, MinSchemaVersion, t.SchemaVersion),
		)
	}

	// 2. Required fields: trace_id non-empty
	if strings.TrimSpace(t.TraceID) == "" {
		return types.NewRPCError(
			types.ErrInvalidTrace,
			"trace missing required field: trace_id",
			types.ErrTypeInvalidTrace,
			false,
			"Every trace must include a non-empty trace_id string.",
		)
	}

	// 2. Required fields: output non-nil and non-empty JSON object
	if len(t.Output) == 0 {
		return types.NewRPCError(
			types.ErrInvalidTrace,
			"trace missing required field: output",
			types.ErrTypeInvalidTrace,
			false,
			"Every trace must include an output object with at least one field.",
		)
	}
	var outputMap map[string]json.RawMessage
	if err := json.Unmarshal(t.Output, &outputMap); err != nil || len(outputMap) == 0 {
		return types.NewRPCError(
			types.ErrInvalidTrace,
			"trace output must be a non-empty JSON object",
			types.ErrTypeInvalidTrace,
			false,
			"The output field must be a JSON object with at least one field.",
		)
	}

	// 3. Size limits: trace JSON size <= 10MB
	traceBytes, err := json.Marshal(t)
	if err != nil {
		return types.NewRPCError(
			types.ErrInvalidTrace,
			"trace could not be serialized for size check",
			types.ErrTypeInvalidTrace,
			false,
			"Ensure all trace fields contain valid JSON-serializable values.",
		)
	}
	if len(traceBytes) > MaxTraceSize {
		return types.NewRPCError(
			types.ErrInvalidTrace,
			fmt.Sprintf("trace exceeds max size: %d > %d bytes", len(traceBytes), MaxTraceSize),
			types.ErrTypeInvalidTrace,
			false,
			fmt.Sprintf("Reduce trace size by filtering steps or truncating tool results. Max allowed: %d bytes (10 MB).", MaxTraceSize),
		)
	}

	// 3. Size limits: steps count <= 10000
	if len(t.Steps) > MaxStepsPerTrace {
		return types.NewRPCError(
			types.ErrInvalidTrace,
			fmt.Sprintf("trace exceeds max steps: %d > %d", len(t.Steps), MaxStepsPerTrace),
			types.ErrTypeInvalidTrace,
			false,
			fmt.Sprintf("Reduce the number of steps to %d or fewer. Consider batching or summarizing intermediate steps.", MaxStepsPerTrace),
		)
	}

	// 4. Step validation
	for _, step := range t.Steps {
		if strings.TrimSpace(step.Name) == "" {
			return types.NewRPCError(
				types.ErrInvalidTrace,
				"trace step missing required field: name",
				types.ErrTypeInvalidTrace,
				false,
				"Every step must include a non-empty name string.",
			)
		}
		if _, ok := validStepTypes[step.Type]; !ok {
			return types.NewRPCError(
				types.ErrInvalidTrace,
				fmt.Sprintf("trace step '%s' has invalid type '%s'", step.Name, step.Type),
				types.ErrTypeInvalidTrace,
				false,
				fmt.Sprintf("Step type must be one of: llm_call, tool_call, retrieval, agent_call. Got '%s' for step '%s'.", step.Type, step.Name),
			)
		}
		// E4: Enforce MaxStepPayload (1 MB) per step.
		stepBytes, err := json.Marshal(step)
		if err != nil {
			return types.NewRPCError(
				types.ErrInvalidTrace,
				fmt.Sprintf("trace step '%s' could not be serialized for size check", step.Name),
				types.ErrTypeInvalidTrace,
				false,
				"Ensure all step fields contain valid JSON-serializable values.",
			)
		}
		if len(stepBytes) > MaxStepPayload {
			return types.NewRPCError(
				types.ErrInvalidTrace,
				fmt.Sprintf("trace step '%s' exceeds max payload size: %d > %d bytes", step.Name, len(stepBytes), MaxStepPayload),
				types.ErrTypeInvalidTrace,
				false,
				fmt.Sprintf("Reduce the step payload size to %d bytes (1 MB) or fewer by truncating tool results or outputs.", MaxStepPayload),
			)
		}
	}

	// 5. Sub-trace depth: recursively check agent_call sub_traces, max depth 5
	if depth >= MaxSubTraceDepth {
		return types.NewRPCError(
			types.ErrInvalidTrace,
			fmt.Sprintf("trace nesting depth %d exceeds maximum %d", depth, MaxSubTraceDepth),
			types.ErrTypeInvalidTrace,
			false,
			fmt.Sprintf("Reduce the agent_call nesting depth to %d or fewer levels.", MaxSubTraceDepth),
		)
	}

	for _, step := range t.Steps {
		if step.Type == types.StepTypeAgentCall && step.SubTrace != nil {
			if rpcErr := validateAtDepth(step.SubTrace, depth+1); rpcErr != nil {
				return rpcErr
			}
		}
	}

	return nil
}
