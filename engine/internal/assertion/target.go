package assertion

import (
	"github.com/segmentio/encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// stepFilterRegex matches patterns like steps[?name=='lookup_order'].result
var stepFilterRegex = regexp.MustCompile(`^steps\[\?name=='([^']+)'\]\.(.+)$`)

// ResolveTarget resolves a JSONPath-like target string against a trace.
// Returns the resolved value as json.RawMessage, or error if not found.
//
// Supported targets:
//   - "output" → trace.Output
//   - "output.message" → trace.Output["message"]
//   - "output.structured" → trace.Output["structured"]
//   - "output.structured.<field>" → trace.Output["structured"]["<field>"]
//   - "steps[?name=='<name>'].args" → first matching step's args
//   - "steps[?name=='<name>'].result" → first matching step's result
//   - "steps[?name=='<name>'].result.<field>" → nested field in step result
func ResolveTarget(trace *types.Trace, target string) (json.RawMessage, error) {
	if target == "output" {
		return trace.Output, nil
	}
	if strings.HasPrefix(target, "output.") {
		return resolveOutputField(trace, target[7:])
	}
	if m := stepFilterRegex.FindStringSubmatch(target); m != nil {
		stepName := m[1]
		field := m[2]
		return resolveStepField(trace, stepName, field)
	}
	return nil, fmt.Errorf("unsupported target: %s", target)
}

// ResolveTargetString resolves a target to a string value.
func ResolveTargetString(trace *types.Trace, target string) (string, error) {
	raw, err := ResolveTarget(trace, target)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return strings.Trim(string(raw), "\""), nil
	}
	return s, nil
}

// resolveOutputField navigates dot-separated fields into trace.Output.
func resolveOutputField(trace *types.Trace, fieldPath string) (json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(trace.Output, &root); err != nil {
		return nil, fmt.Errorf("cannot parse output as object: %v", err)
	}
	return navigateDotPath(root, fieldPath, "output")
}

// resolveStepField finds the first step with the given name and navigates into args or result.
func resolveStepField(trace *types.Trace, stepName string, fieldPath string) (json.RawMessage, error) {
	var step *types.Step
	for i := range trace.Steps {
		if trace.Steps[i].Name == stepName {
			step = &trace.Steps[i]
			break
		}
	}
	if step == nil {
		return nil, fmt.Errorf("step not found: %s", stepName)
	}

	parts := strings.SplitN(fieldPath, ".", 2)
	topField := parts[0]

	var topRaw json.RawMessage
	switch topField {
	case "args":
		topRaw = step.Args
	case "result":
		topRaw = step.Result
	default:
		return nil, fmt.Errorf("unsupported step field: %s (must be args or result)", topField)
	}

	if len(parts) == 1 {
		return topRaw, nil
	}

	var nested map[string]json.RawMessage
	if err := json.Unmarshal(topRaw, &nested); err != nil {
		return nil, fmt.Errorf("cannot parse step %s.%s as object: %v", stepName, topField, err)
	}
	return navigateDotPath(nested, parts[1], fmt.Sprintf("steps[?name=='%s'].%s", stepName, topField))
}

// navigateDotPath traverses a map following a dot-separated key path.
func navigateDotPath(root map[string]json.RawMessage, path string, parentDesc string) (json.RawMessage, error) {
	parts := strings.SplitN(path, ".", 2)
	key := parts[0]

	val, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("field %q not found in %s", key, parentDesc)
	}

	if len(parts) == 1 {
		return val, nil
	}

	var nested map[string]json.RawMessage
	if err := json.Unmarshal(val, &nested); err != nil {
		return nil, fmt.Errorf("cannot parse %s.%s as object: %v", parentDesc, key, err)
	}
	return navigateDotPath(nested, parts[1], parentDesc+"."+key)
}
