package assertion

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/attest-ai/attest/engine/pkg/types"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/segmentio/encoding/json"
)

// schemaCache is a process-level cache of compiled JSON schemas keyed by SHA-256 of the raw schema bytes.
var schemaCache sync.Map // map[string]*jsonschema.Schema

// SchemaEvaluator implements Layer 1: JSON Schema validation.
type SchemaEvaluator struct{}

func (e *SchemaEvaluator) Evaluate(trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	start := time.Now()

	var spec struct {
		Target string          `json:"target"`
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(assertion.Spec, &spec); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid schema spec: %v", err))
	}
	if spec.Target == "" {
		return failResult(assertion, start, "schema spec missing required field: target")
	}
	if len(spec.Schema) == 0 {
		return failResult(assertion, start, "schema spec missing required field: schema")
	}

	targetValue, err := ResolveTarget(trace, spec.Target)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("target resolution failed: %v", err))
	}

	// Unmarshal schema doc into any so AddResource accepts it.
	var schemaDoc any
	if err := json.Unmarshal(spec.Schema, &schemaDoc); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid JSON schema: %v", err))
	}

	// Cache compiled schemas keyed by SHA-256 of raw schema bytes.
	cacheKey := fmt.Sprintf("%x", sha256.Sum256(spec.Schema))
	var schema *jsonschema.Schema
	if cached, ok := schemaCache.Load(cacheKey); ok {
		schema = cached.(*jsonschema.Schema)
	} else {
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("schema.json", schemaDoc); err != nil {
			return failResult(assertion, start, fmt.Sprintf("schema compilation failed: %v", err))
		}
		compiled, err := compiler.Compile("schema.json")
		if err != nil {
			return failResult(assertion, start, fmt.Sprintf("schema compilation failed: %v", err))
		}
		schema = compiled
		schemaCache.Store(cacheKey, schema)
	}

	var value any
	if err := json.Unmarshal(targetValue, &value); err != nil {
		return failResult(assertion, start, fmt.Sprintf("cannot parse target value: %v", err))
	}

	expected := fmt.Sprintf("matches JSON Schema (sha256:%s)", cacheKey[:8])
	actual := truncate(string(targetValue))

	if err := schema.Validate(value); err != nil {
		return &types.AssertionResult{
			AssertionID:     assertion.AssertionID,
			Status:          types.StatusHardFail,
			Score:           0.0,
			Explanation:     fmt.Sprintf("%s failed schema validation: %v", spec.Target, err),
			DurationMS:      time.Since(start).Milliseconds(),
			RequestID:       assertion.RequestID,
			TraceNodePath:   spec.Target,
			Expected:        expected,
			Actual:          actual,
			SuggestedAction: "Inspect the target value and align it with the schema, or relax the schema if the field is optional.",
		}
	}

	return &types.AssertionResult{
		AssertionID:   assertion.AssertionID,
		Status:        types.StatusPass,
		Score:         1.0,
		Explanation:   fmt.Sprintf("%s matches schema: all required fields present, types valid.", spec.Target),
		DurationMS:    time.Since(start).Milliseconds(),
		RequestID:     assertion.RequestID,
		TraceNodePath: spec.Target,
		Expected:      expected,
		Actual:        actual,
	}
}

// failResult constructs a hard_fail AssertionResult with the given explanation.
func failResult(assertion *types.Assertion, start time.Time, explanation string) *types.AssertionResult {
	return &types.AssertionResult{
		AssertionID: assertion.AssertionID,
		Status:      types.StatusHardFail,
		Score:       0.0,
		Explanation: explanation,
		DurationMS:  time.Since(start).Milliseconds(),
		RequestID:   assertion.RequestID,
	}
}

// passResult constructs a pass AssertionResult with score 1.0.
func passResult(assertion *types.Assertion, start time.Time, explanation string) *types.AssertionResult {
	return &types.AssertionResult{
		AssertionID: assertion.AssertionID,
		Status:      types.StatusPass,
		Score:       1.0,
		Explanation: explanation,
		DurationMS:  time.Since(start).Milliseconds(),
		RequestID:   assertion.RequestID,
	}
}
