package capture

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
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

// timestampKeys hold points in time: numbers (seconds, ms, µs or ns since the
// epoch, told apart by magnitude) or digit strings. ISO-8601 strings anywhere
// are timestamps too.
//
// Every timestamp occurrence in the fixture gets its own sequence number:
// occurrences are sorted by their original instant, then by role (start-like
// keys before others before end-like keys, so a zero-length step still starts
// before it ends), then by position in the sorted-key walk, and numbered
// 1, 2, 3, ... Numbers keep the original order, so every start stays before
// its end. Equal instants are never merged: whether two events land in the
// same millisecond or second in one run and not in the next cannot change the
// output. ISO strings become {{timestamp_N}} from the same numbering.
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
	traceIDs map[string]string
	spanIDs  map[string]string
	strs     map[string]string // provider id or email → placeholder
	counts   map[string]int
	// stamps are the timestamp occurrences in walk order; numbers[i] is the
	// sequence number of stamps[i]. next is the rewrite pass's cursor.
	stamps  []stamp
	numbers []int
	next    int
}

// stamp is one timestamp occurrence.
type stamp struct {
	instant *big.Rat // nanoseconds since the epoch
	role    int      // 0 start-like key, 1 other, 2 end-like key
}

// Template rewrites doc (decoded with json.Decoder.UseNumber) in place and
// returns the result. Map keys are visited in sorted order, so placeholder
// numbering depends only on the document's content.
func Template(doc any) any {
	t := &templater{
		traceIDs: map[string]string{},
		spanIDs:  map[string]string{},
		strs:     map[string]string{},
		counts:   map[string]int{},
	}
	t.collect(doc, "")
	t.numberStamps()
	return t.rewrite(doc, "")
}

// numberStamps orders the occurrences by (instant, role, position) and gives
// each its 1-based rank.
func (t *templater) numberStamps() {
	order := make([]int, len(t.stamps))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := t.stamps[order[a]], t.stamps[order[b]]
		if c := x.instant.Cmp(y.instant); c != 0 {
			return c < 0
		}
		return x.role < y.role
	})
	t.numbers = make([]int, len(t.stamps))
	for rank, i := range order {
		t.numbers[i] = rank + 1
	}
}

// roleOf ranks a key: start-like keys before others before end-like keys.
func roleOf(key string) int {
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

// epochNanos reads a number of seconds, ms, µs or ns since the epoch,
// telling the unit apart by magnitude.
func epochNanos(num string) (*big.Rat, bool) {
	r, ok := new(big.Rat).SetString(num)
	if !ok {
		return nil, false
	}
	abs := new(big.Rat).Abs(r)
	var unit int64
	switch {
	case abs.Cmp(big.NewRat(1e11, 1)) < 0:
		unit = 1e9
	case abs.Cmp(big.NewRat(1e14, 1)) < 0:
		unit = 1e6
	case abs.Cmp(big.NewRat(1e17, 1)) < 0:
		unit = 1e3
	default:
		unit = 1
	}
	return r.Mul(r, big.NewRat(unit, 1)), true
}

var (
	zoneNoColon = regexp.MustCompile(`[+-]\d{4}$`)
	zoneColon   = regexp.MustCompile(`[+-]\d{2}:\d{2}$`)
)

// isoNanos parses an ISO-8601 match: a space for T, no zone (UTC) and a
// +hhmm zone are accepted.
func isoNanos(s string) (*big.Rat, bool) {
	v := strings.Replace(s, " ", "T", 1)
	if m := zoneNoColon.FindStringIndex(v); m != nil {
		v = v[:m[0]+3] + ":" + v[m[0]+3:]
	}
	if !strings.HasSuffix(v, "Z") && !zoneColon.MatchString(v) {
		v += "Z"
	}
	tm, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return nil, false
	}
	return new(big.Rat).SetInt(big.NewInt(0).Add(big.NewInt(tm.Unix()*1e9), big.NewInt(int64(tm.Nanosecond())))), true
}

// stampsIn rewrites the timestamps held by a string: the whole string under
// a timestamp key when it is digits, else ISO-8601 matches and the timestamp
// fields of JSON held as text (an event stream), in that order. Pass 1
// records occurrences; pass 2 (numbered) replaces them.
func (t *templater) stampsIn(s, key string, numbered bool) string {
	if timestampKeys[key] && isDigits(s) {
		if ns, ok := epochNanos(s); ok {
			return t.stampAt(stamp{ns, roleOf(key)}, numbered, func(n int) string { return fmt.Sprint(n) }, s)
		}
	}
	s = isoTimePattern.ReplaceAllStringFunc(s, func(m string) string {
		ns, ok := isoNanos(m)
		if !ok {
			return m
		}
		return t.stampAt(stamp{ns, roleOf(key)}, numbered, func(n int) string { return fmt.Sprintf("{{timestamp_%d}}", n) }, m)
	})
	return sseTimestampPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := sseTimestampPattern.FindStringSubmatch(m)
		ns, ok := epochNanos(sub[2])
		if !ok {
			return m
		}
		return t.stampAt(stamp{ns, roleOf(sub[1])}, numbered, func(n int) string { return `"` + sub[1] + `":` + fmt.Sprint(n) }, m)
	})
}

// stampAt records an occurrence (pass 1) or returns its replacement (pass 2).
func (t *templater) stampAt(st stamp, numbered bool, render func(int) string, original string) string {
	if !numbered {
		t.stamps = append(t.stamps, st)
		return original
	}
	n := t.numbers[t.next]
	t.next++
	return render(n)
}

// collect assigns trace and span id placeholders in visit order.
func (t *templater) collect(v any, key string) {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
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
		default:
			t.stampsIn(x, key, false)
		}
	case json.Number:
		if timestampKeys[key] {
			if ns, ok := epochNanos(x.String()); ok {
				t.stampAt(stamp{ns, roleOf(key)}, false, nil, "")
			}
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
			if ns, ok := epochNanos(x.String()); ok {
				return json.Number(t.stampAt(stamp{ns, roleOf(key)}, true, func(n int) string { return fmt.Sprint(n) }, ""))
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
	}
	// Timestamps first, on the string pass 1 saw.
	s = t.stampsIn(s, key, true)
	s = providerIDPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := providerIDPattern.FindStringSubmatch(m)
		if !strings.ContainsFunc(sub[2], unicode.IsDigit) {
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
