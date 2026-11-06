package assertion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/attest-framework/attest/engine/internal/llm"
	"github.com/attest-framework/attest/engine/internal/world"
	"github.com/attest-framework/attest/engine/pkg/types"
)

// EffectEvaluator evaluates effect assertions, Layer 3: deterministic checks
// of what an agent did to the external system, run against a world-v1
// snapshot the SDK embeds in the spec. The trace is not consulted; the
// snapshot is the evidence.
type EffectEvaluator struct{}

// effectSpec is the spec of an effect assertion.
//
//	{"check": "count", "world": <snapshot>, "resource": "refunds",
//	 "where": {"order_id": "O-1042"}, "equals": 1}
//	{"check": "unique", "world": <snapshot>, "resource": "refunds", "fields": ["order_id"]}
//	{"check": "idempotent", "world": <snapshot>, "resource": "refunds", "field": "idempotency_key"}
//	{"check": "sum", "world": <snapshot>, "resource": "refunds", "field": "amount_cents",
//	 "at_most": {"related": {"resource": "orders", "field": "amount_cents", "on": "order_id"}}}
//	{"check": "matches", "world": <snapshot>, "resource": "notifications", "field": "customer_id",
//	 "related": {"resource": "orders", "field": "customer_id", "on": "order_id"}}
type effectSpec struct {
	Check    string          `json:"check"`
	World    json.RawMessage `json:"world"`
	Resource string          `json:"resource"`
	Where    map[string]any  `json:"where"`
	Equals   *int            `json:"equals"`
	AtMost   *atMostBound    `json:"at_most"`
	Related  *relatedRef     `json:"related"`
	AtLeast  *int            `json:"at_least"`
	Fields   []string        `json:"fields"`
	Field    string          `json:"field"`
}

// Evaluate implements Evaluator.
func (e *EffectEvaluator) Evaluate(_ *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	start := time.Now()
	var spec effectSpec
	dec := json.NewDecoder(bytes.NewReader(assertion.Spec))
	dec.UseNumber()
	if err := dec.Decode(&spec); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid effect spec: %v", err))
	}
	if spec.Check == "" || spec.Resource == "" {
		return failResult(assertion, start, "effect spec missing required field: check and resource")
	}
	if len(spec.World) == 0 {
		return failResult(assertion, start, "effect spec missing required field: world")
	}
	if string(spec.World) == "null" {
		return notEvaluatedResult(assertion, start, types.ReasonMissingEvidence,
			"no world snapshot: run the test with the world helper (attest_world / useWorld) so the effect has state to check")
	}
	if err := world.ValidateSnapshot(spec.World); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid effect spec: world is not a world-v1 snapshot: %v", err))
	}
	var snap struct {
		State map[string][]map[string]any `json:"state"`
	}
	sdec := json.NewDecoder(bytes.NewReader(spec.World))
	sdec.UseNumber()
	if err := sdec.Decode(&snap); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid effect spec: %v", err))
	}
	records, ok := snap.State[spec.Resource]
	if !ok {
		return failResult(assertion, start, fmt.Sprintf("effect spec: resource %q is not in the world (resources: %s)", spec.Resource, strings.Join(sortedKeys(snap.State), ", ")))
	}

	var v *effectViolation
	var pass string
	var err error
	switch spec.Check {
	case "count":
		v, pass, err = checkCount(&spec, records)
	case "unique":
		v, pass, err = checkUnique(&spec, records)
	case "idempotent":
		v, pass, err = checkIdempotent(&spec, records)
	case "sum":
		v, pass, err = checkSum(&spec, records, snap.State)
	case "matches":
		v, pass, err = checkMatches(&spec, records, snap.State)
	default:
		err = fmt.Errorf("unsupported effect check: %s", spec.Check)
	}
	var evidence *evidenceError
	switch {
	case errors.As(err, &evidence):
		return failResult(assertion, start, shown(evidence.Error()))
	case err != nil:
		return failResult(assertion, start, fmt.Sprintf("invalid effect spec: %v", err))
	case v != nil:
		return failResultWithDiag(assertion, start, types.StatusHardFail, 0.0, shown(v.message), diagFields{
			target: "world." + shown(spec.Resource), expected: shown(v.expected), actual: shown(v.actual),
		})
	}
	return passResult(assertion, start, shown(pass))
}

type effectViolation struct{ message, expected, actual string }

func checkCount(spec *effectSpec, records []map[string]any) (*effectViolation, string, error) {
	if spec.Equals == nil && spec.AtMost == nil && spec.AtLeast == nil {
		return nil, "", fmt.Errorf("count needs equals, at_most or at_least")
	}
	if spec.Equals != nil && (spec.AtMost != nil || spec.AtLeast != nil) {
		return nil, "", fmt.Errorf("count: equals excludes at_most and at_least")
	}
	var atMost *int
	if spec.AtMost != nil {
		n, err := spec.AtMost.count()
		if err != nil {
			return nil, "", err
		}
		atMost = &n
	}
	n := 0
	for _, r := range records {
		if matches(r, spec.Where) {
			n++
		}
	}
	subject := fmt.Sprintf("%d %s%s", n, noun(spec.Resource, n), whereText(spec.Where))
	switch {
	case spec.Equals != nil && n != *spec.Equals:
		return violation(subject, fmt.Sprintf("expected %d", *spec.Equals), *spec.Equals, n), "", nil
	case atMost != nil && n > *atMost:
		return violation(subject, fmt.Sprintf("expected at most %d", *atMost), *atMost, n), "", nil
	case spec.AtLeast != nil && n < *spec.AtLeast:
		return violation(subject, fmt.Sprintf("expected at least %d", *spec.AtLeast), *spec.AtLeast, n), "", nil
	}
	return nil, "effect holds: " + subject, nil
}

func violation(subject, expectation string, want, got int) *effectViolation {
	return &effectViolation{
		message:  fmt.Sprintf("effect violated: %s (%s)", subject, expectation),
		expected: fmt.Sprintf("%d", want),
		actual:   fmt.Sprintf("%d", got),
	}
}

func checkUnique(spec *effectSpec, records []map[string]any) (*effectViolation, string, error) {
	if len(spec.Fields) == 0 {
		return nil, "", fmt.Errorf("unique needs fields")
	}
	groups := map[string]int{}
	labels := map[string]string{}
	var order []string
	for _, r := range records {
		if !matches(r, spec.Where) {
			continue
		}
		keyParts := make([]string, len(spec.Fields))
		labelParts := make([]string, len(spec.Fields))
		for i, f := range spec.Fields {
			v, present := r[f]
			keyParts[i] = fmt.Sprintf("%t:%s", present, canonicalJSON(v))
			labelParts[i] = f + "=" + display(v)
		}
		key := strings.Join(keyParts, "\x00")
		if groups[key] == 0 {
			order = append(order, key)
			labels[key] = strings.Join(labelParts, ", ")
		}
		groups[key]++
	}
	var dups []string
	for _, k := range order {
		if groups[k] > 1 {
			dups = append(dups, k)
		}
	}
	by := strings.Join(spec.Fields, ", ")
	if len(dups) == 0 {
		return nil, fmt.Sprintf("effect holds: %s unique by %s", spec.Resource, by), nil
	}
	first := dups[0]
	msg := fmt.Sprintf("effect violated: %d %s share %s (expected unique by %s)", groups[first], spec.Resource, labels[first], by)
	if len(dups) > 1 {
		msg += fmt.Sprintf("; %d more duplicate groups", len(dups)-1)
	}
	return &effectViolation{message: msg, expected: "1 per " + by, actual: fmt.Sprintf("%d for %s", groups[first], labels[first])}, "", nil
}

func checkIdempotent(spec *effectSpec, records []map[string]any) (*effectViolation, string, error) {
	field := spec.Field
	if field == "" {
		return nil, "", fmt.Errorf("idempotent needs field, the record field that stores the idempotency key")
	}
	keyed := map[string]int{}
	missing, firstMissing := 0, 0
	n := 0
	for i, r := range records {
		if !matches(r, spec.Where) {
			continue
		}
		n++
		k, _ := r[field].(string)
		if k == "" {
			if missing == 0 {
				firstMissing = i + 1
			}
			missing++
			continue
		}
		keyed[k]++
	}
	if missing > 0 {
		return &effectViolation{
			message:  fmt.Sprintf("effect violated: %d of %d %s created without an idempotency key (%s; first is record %d)", missing, n, spec.Resource, field, firstMissing),
			expected: "every record carries " + field,
			actual:   fmt.Sprintf("%d without", missing),
		}, "", nil
	}
	keys := sortedKeys(keyed)
	for _, k := range keys {
		if keyed[k] > 1 {
			return &effectViolation{
				message:  fmt.Sprintf("effect violated: %d %s share idempotency key %s (%s)", keyed[k], spec.Resource, k, field),
				expected: "one record per key", actual: fmt.Sprintf("%d for %s", keyed[k], k),
			}, "", nil
		}
	}
	return nil, fmt.Sprintf("effect holds: %d %s, each with its own idempotency key", n, spec.Resource), nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// noun is the resource name, singular for a count of one.
func noun(resource string, n int) string {
	if n == 1 && strings.HasSuffix(resource, "s") {
		return strings.TrimSuffix(resource, "s")
	}
	return resource
}

// whereText renders a filter: " for O-1042" for one field, " for a=1, b=2"
// for several, nothing for none.
func whereText(where map[string]any) string {
	switch len(where) {
	case 0:
		return ""
	case 1:
		for _, v := range where {
			return " for " + display(v)
		}
	}
	keys := sortedKeys(where)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + display(where[k])
	}
	return " for " + strings.Join(parts, ", ")
}

// maxShownValue bounds one record value in an explanation.
const maxShownValue = 64

// shown makes text from records, specs or the agent safe to print: control
// and format characters (ESC, bidi overrides such as U+202E) are removed and
// the length is cut.
func shown(s string) string { return truncate(llm.StripControl(s)) }

func display(v any) string {
	var out string
	switch t := v.(type) {
	case string:
		out = t
	case nil:
		out = "null"
	case json.Number:
		out = t.String()
	default:
		b, _ := json.Marshal(v)
		out = string(b)
	}
	out = llm.StripControl(out)
	if r := []rune(out); len(r) > maxShownValue {
		out = string(r[:maxShownValue]) + "..."
	}
	return out
}

func matches(record map[string]any, where map[string]any) bool {
	for k, want := range where {
		got, ok := record[k]
		if !ok || !jsonEqual(got, want) {
			return false
		}
	}
	return true
}
