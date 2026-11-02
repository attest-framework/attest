package capture

import (
	"encoding/json"
	"fmt"
	"math/big"
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

// timestampKeys hold numeric points in time. They become order-preserving
// ordinals: the distinct values sorted ascending map to 1, 2, 3, ...
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
	// tool call ids. A match must contain a digit, so tool names such as
	// call_customer are left alone.
	providerIDPattern = regexp.MustCompile(`\b(chatcmpl-|resp_|msg_|call_|toolu_|fc_|rs_)([A-Za-z0-9_-]{8,})`)
	emailPattern      = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	isoTimePattern    = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	// sseTimestampPattern finds timestamp fields inside a JSON document held
	// as text, such as a server-sent event stream.
	sseTimestampPattern = regexp.MustCompile(`"(created|created_at|completed_at)":\s*(\d+)`)
)

// templater holds the value → placeholder maps for one fixture.
type templater struct {
	traceIDs   map[string]string
	spanIDs    map[string]string
	timestamps map[string]string // decimal value → ordinal
	strs       map[string]string // provider id, email, ISO time → placeholder
	counts     map[string]int
}

// Template rewrites doc (decoded with json.Decoder.UseNumber) in place and
// returns the result. Map keys are visited in sorted order, so placeholder
// numbering depends only on the document's content.
func Template(doc any) any {
	t := &templater{
		traceIDs:   map[string]string{},
		spanIDs:    map[string]string{},
		timestamps: map[string]string{},
		strs:       map[string]string{},
		counts:     map[string]int{},
	}
	var stamps []string
	t.collect(doc, "", &stamps)
	t.rankTimestamps(stamps)
	return t.rewrite(doc, "")
}

// collect assigns trace and span id placeholders in visit order and gathers
// every timestamp value.
func (t *templater) collect(v any, key string, stamps *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
			t.collect(x[k], k, stamps)
		}
	case []any:
		for _, e := range x {
			t.collect(e, key, stamps)
		}
	case string:
		switch {
		case traceIDKeys[key] && x != "":
			t.idFor(t.traceIDs, x, "trace", 32)
		case spanIDKeys[key] && x != "":
			t.idFor(t.spanIDs, x, "span", 16)
		case timestampKeys[key] && isDigits(x):
			*stamps = append(*stamps, x)
		default:
			for _, m := range sseTimestampPattern.FindAllStringSubmatch(x, -1) {
				*stamps = append(*stamps, m[2])
			}
		}
	case json.Number:
		if timestampKeys[key] {
			*stamps = append(*stamps, x.String())
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

// rankTimestamps maps each distinct timestamp to its 1-based rank in
// ascending numeric order.
func (t *templater) rankTimestamps(stamps []string) {
	distinct := map[string]*big.Rat{}
	for _, s := range stamps {
		if _, ok := distinct[s]; ok {
			continue
		}
		r, ok := new(big.Rat).SetString(s)
		if ok {
			distinct[s] = r
		}
	}
	keys := make([]string, 0, len(distinct))
	for k := range distinct {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if c := distinct[keys[i]].Cmp(distinct[keys[j]]); c != 0 {
			return c < 0
		}
		return keys[i] < keys[j]
	})
	for i, k := range keys {
		t.timestamps[k] = fmt.Sprint(i + 1)
	}
}

func (t *templater) rewrite(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
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
			if p, ok := t.timestamps[x.String()]; ok {
				return json.Number(p)
			}
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
		if p, ok := t.timestamps[s]; ok {
			return p
		}
	}
	s = providerIDPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := providerIDPattern.FindStringSubmatch(m)
		if !strings.ContainsFunc(sub[2], unicode.IsDigit) {
			return m
		}
		return sub[1] + t.placeholder("id", m)
	})
	s = emailPattern.ReplaceAllStringFunc(s, func(m string) string { return t.placeholder("email", m) })
	s = isoTimePattern.ReplaceAllStringFunc(s, func(m string) string { return t.placeholder("timestamp", m) })
	s = sseTimestampPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := sseTimestampPattern.FindStringSubmatch(m)
		if p, ok := t.timestamps[sub[2]]; ok {
			return `"` + sub[1] + `":` + p
		}
		return m
	})
	return s
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
