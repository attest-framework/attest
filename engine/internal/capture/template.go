package capture

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Templating replaces values that change between two runs of the same
// scenario with stable placeholders, so a re-capture produces the same bytes
// and replay keys do not depend on run-specific values. One templater is
// applied to the whole fixture (trace and exchanges), so the same value maps
// to the same placeholder everywhere.

// Keys whose string values are OpenTelemetry trace ids or span ids. Templated
// ids stay valid lowercase hex of the original width (trace-v2 requires a
// 16-digit hex span_id).
var (
	traceIDKeys = map[string]bool{"trace_id": true, "parent_trace_id": true, "traceId": true}
	spanIDKeys  = map[string]bool{"span_id": true, "parent_span_id": true, "spanId": true, "parentSpanId": true}
)

// timestampKeys hold points in time, as numbers or digit strings. ISO-8601
// strings anywhere, and the created/created_at/completed_at fields of JSON
// held as text (an event stream), are timestamps too.
//
// Timestamps are numbered by position, never by value: the document is
// walked in a fixed order (arrays by index; in each object start-like keys,
// then the other keys by name, then end-like keys) and each timestamp
// occurrence takes the next number, 1, 2, 3, ... The output depends only on
// the document's shape, so two runs whose events are 1 ms apart in one and
// coincide in the other give the same bytes. Every object's start is
// numbered before its end, and a parent's start before its children, which
// come before the parent's end. Numeric fields become the number; ISO
// strings become {{timestamp_N}}.
var timestampKeys = map[string]bool{
	"started_at_ms": true, "ended_at_ms": true, "timestamp": true, "timestamp_ms": true,
	"created": true, "created_at": true, "completed_at": true,
	"startTimeUnixNano": true, "endTimeUnixNano": true,
}

// durationKeys hold elapsed times. They become 0.
var durationKeys = map[string]bool{
	"latency_ms": true, "duration_ms": true, "total_duration_ms": true,
	"first_byte_ms": true, "elapsed_ms": true,
}

var (
	// providerIDPattern matches provider-generated ids: chat completions,
	// Responses objects and output items, Anthropic messages and tool uses,
	// tool call ids. In free text a match must contain a digit, so tool
	// names such as call_customer are left alone; a value seen in an id
	// field (idKeys, idInTextPattern) is an id even without a digit.
	providerIDPattern = regexp.MustCompile(`\b(chatcmpl-|resp_|msg_|call_|toolu_|fc_|rs_)([A-Za-z0-9_-]{8,})`)
	// idInTextPattern finds provider ids held in id fields of JSON stored as
	// text: OpenTelemetry message attributes and event-stream bodies.
	idInTextPattern = regexp.MustCompile(`"(?:id|tool_call_id|call_id|tool_use_id|item_id|response_id)"\s*:\s*"((?:chatcmpl-|resp_|msg_|call_|toolu_|fc_|rs_)[A-Za-z0-9_-]{8,})"`)
	emailPattern    = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	isoTimePattern  = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	// sseTimestampPattern finds timestamp fields inside a JSON document held
	// as text, such as a server-sent event stream.
	sseTimestampPattern = regexp.MustCompile(`"(created|created_at|completed_at)":\s*(\d+)`)
)

// idKeys hold a provider id as their whole value.
var idKeys = map[string]bool{
	"id": true, "tool_call_id": true, "call_id": true, "tool_use_id": true,
	"item_id": true, "response_id": true, "gen_ai.tool.call.id": true,
}

// templater holds the value → placeholder maps for one fixture.
type templater struct {
	traceIDs map[string]string
	spanIDs  map[string]string
	strs     map[string]string // provider id or email → placeholder
	knownIDs map[string]bool   // provider ids seen in id fields
	counts   map[string]int
}

// Template rewrites doc (decoded with json.Decoder.UseNumber) in place and
// returns the result. Placeholder numbering depends only on the document's
// content and shape.
func Template(doc any) any {
	t := &templater{
		traceIDs: map[string]string{},
		spanIDs:  map[string]string{},
		strs:     map[string]string{},
		knownIDs: map[string]bool{},
		counts:   map[string]int{},
	}
	t.collect(doc, "")
	return t.rewrite(doc, "")
}

// keyRole places a key in the walk: start-like keys first, end-like keys
// last.
func keyRole(key string) int {
	k := strings.ToLower(key)
	switch {
	case strings.HasPrefix(k, "start"):
		return 0
	case strings.HasPrefix(k, "end"):
		return 2
	default:
		return 1
	}
}

// walkKeys orders an object's keys: start-like, then the rest, then
// end-like, each group by name.
func walkKeys(m map[string]any) []string {
	keys := sortedKeys(m)
	sort.SliceStable(keys, func(i, j int) bool { return keyRole(keys[i]) < keyRole(keys[j]) })
	return keys
}

// collect assigns trace and span id placeholders in walk order and records
// the provider ids that appear in id fields.
func (t *templater) collect(v any, key string) {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range walkKeys(x) {
			t.collect(x[k], k)
		}
	case []any:
		for _, e := range x {
			t.collect(e, key)
		}
	case string:
		switch {
		case traceIDKeys[key] && x != "":
			t.idFor(t.traceIDs, x, "trace", 32)
		case spanIDKeys[key] && x != "":
			t.idFor(t.spanIDs, x, "span", 16)
		case idKeys[key] && providerIDPattern.FindString(x) == x:
			t.knownIDs[x] = true
		}
		for _, sub := range idInTextPattern.FindAllStringSubmatch(x, -1) {
			t.knownIDs[sub[1]] = true
		}
	}
}

// idFor returns the placeholder for id v. A hex id keeps its width; an id
// with a prefix such as the converter's "otel_" keeps the prefix and the
// width of its hex tail; any other id becomes width hex digits.
func (t *templater) idFor(m map[string]string, v, kind string, width int) string {
	if p, ok := m[v]; ok {
		return p
	}
	prefix := ""
	if sub := prefixedHexID.FindStringSubmatch(v); sub != nil {
		prefix, width = sub[1], len(sub[2])
	}
	t.counts[kind]++
	p := prefix + fmt.Sprintf("%0*x", width, t.counts[kind])
	m[v] = p
	return p
}

// prefixedHexID splits an id into an optional word prefix and a hex tail.
var prefixedHexID = regexp.MustCompile(`^([A-Za-z]+_)?([0-9a-f]{8,})$`)

// nextStamp returns the next timestamp number.
func (t *templater) nextStamp() int {
	t.counts["timestamp"]++
	return t.counts["timestamp"]
}

func (t *templater) rewrite(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range walkKeys(x) {
			x[k] = t.rewrite(x[k], k)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = t.rewrite(e, key)
		}
		return x
	case json.Number:
		switch {
		case durationKeys[key]:
			return json.Number("0")
		case timestampKeys[key]:
			return json.Number(fmt.Sprint(t.nextStamp()))
		}
		return x
	case string:
		return t.rewriteString(x, key)
	default:
		return v
	}
}

func (t *templater) rewriteString(s, key string) string {
	switch {
	case traceIDKeys[key]:
		if p, ok := t.traceIDs[s]; ok {
			return p
		}
	case spanIDKeys[key]:
		if p, ok := t.spanIDs[s]; ok {
			return p
		}
	case timestampKeys[key] && isDigits(s):
		return fmt.Sprint(t.nextStamp())
	}
	s = isoTimePattern.ReplaceAllStringFunc(s, func(string) string {
		return fmt.Sprintf("{{timestamp_%d}}", t.nextStamp())
	})
	s = sseTimestampPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := sseTimestampPattern.FindStringSubmatch(m)
		return `"` + sub[1] + `":` + fmt.Sprint(t.nextStamp())
	})
	s = providerIDPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := providerIDPattern.FindStringSubmatch(m)
		if !t.knownIDs[m] && !strings.ContainsFunc(sub[2], unicode.IsDigit) {
			return m
		}
		return sub[1] + t.placeholder("id", m)
	})
	return emailPattern.ReplaceAllStringFunc(s, func(m string) string { return t.placeholder("email", m) })
}

// placeholder returns the stable {{kind_N}} placeholder for value.
func (t *templater) placeholder(kind, value string) string {
	k := kind + "\x00" + value
	if p, ok := t.strs[k]; ok {
		return p
	}
	t.counts[kind]++
	p := fmt.Sprintf("{{%s_%d}}", kind, t.counts[kind])
	t.strs[k] = p
	return p
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
