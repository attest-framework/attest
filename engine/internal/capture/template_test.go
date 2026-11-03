package capture

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func templated(t *testing.T, doc string) string {
	t.Helper()
	v, err := decodeJSON([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	out, err := canonical(Template(v))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestTemplate_IDsKeepWidthAndMapConsistently(t *testing.T) {
	got := templated(t, `{"trace_id":"otel_ab12cd34ef56ab78","steps":[
		{"span_id":"aaaaaaaaaaaaaaaa","parent_span_id":"bbbbbbbbbbbbbbbb"},
		{"span_id":"cccccccccccccccc","parent_span_id":"bbbbbbbbbbbbbbbb"}]}`)

	want := `{"steps":[{"parent_span_id":"0000000000000001","span_id":"0000000000000002"},{"parent_span_id":"0000000000000001","span_id":"0000000000000003"}],"trace_id":"otel_0000000000000001"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestTemplate_ProviderIDsAndEmails_SamePlaceholderEverywhere(t *testing.T) {
	got := templated(t, `{"a":{"id":"chatcmpl-Abc123xyz9","to":"Jane.Doe@Example.co.uk"},
		"b":"tool call_ab12cd34ef from chatcmpl-Abc123xyz9 for Jane.Doe@Example.co.uk",
		"c":"call_customer stays; msg_short stays"}`)

	for _, want := range []string{
		`"id":"chatcmpl-{{id_1}}"`, `"to":"{{email_1}}"`,
		`"b":"tool call_{{id_2}} from chatcmpl-{{id_1}} for {{email_1}}"`,
		`"c":"call_customer stays; msg_short stays"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

func TestTemplate_TimestampsNumberedByOriginalOrder_DurationsZero(t *testing.T) {
	got := templated(t, `{"a":{"created":1700000300,"latency_ms":812},
		"b":[{"started_at_ms":1700000000500,"ended_at_ms":1700000000900},{"started_at_ms":1700000000900}],
		"c":"data: {\"created\": 1700000300, \"n\":1}\n\n","d":"2023-11-14T22:18:20.950Z"}`)

	// Instants: b[0] start 1700000000.5s; b[1] start and b[0] end at .9s
	// (start before end at one instant); a.created and c's created at
	// 1700000300s (walk order between equals); d at 1700000300.95s.
	for _, want := range []string{
		`{"ended_at_ms":3,"started_at_ms":1}`, `{"started_at_ms":2}`, `"created":4`,
		`data: {\"created\":5, \"n\":1}`, `"d":"{{timestamp_6}}"`, `"latency_ms":0`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

// (a) Templated steps and spans keep start before end, a zero-length step
// and span included.
func TestTemplate_StartBeforeEnd_StepsAndSpans_ZeroDuration(t *testing.T) {
	doc := `{"steps":[{"started_at_ms":1700000000100,"ended_at_ms":1700000000300},
		{"started_at_ms":1700000000200,"ended_at_ms":1700000000200},
		{"started_at_ms":1700000000300,"ended_at_ms":1700000000400}],
		"spans":[{"startTimeUnixNano":"1700000000100000000","endTimeUnixNano":"1700000000100000000"},
		{"startTimeUnixNano":"1700000000050000000","endTimeUnixNano":"1700000000500000000"}]}`
	var parsed struct {
		Steps []map[string]json.Number `json:"steps"`
		Spans []map[string]string      `json:"spans"`
	}
	if err := json.Unmarshal([]byte(templated(t, doc)), &parsed); err != nil {
		t.Fatal(err)
	}
	for i, s := range parsed.Steps {
		start, _ := s["started_at_ms"].Int64()
		end, _ := s["ended_at_ms"].Int64()
		if start >= end {
			t.Errorf("step %d: started %d, ended %d", i, start, end)
		}
	}
	for i, s := range parsed.Spans {
		start, _ := strconv.Atoi(s["startTimeUnixNano"])
		end, _ := strconv.Atoi(s["endTimeUnixNano"])
		if start >= end {
			t.Errorf("span %d: start %d, end %d", i, start, end)
		}
	}
}

// (b) Different absolute times with the same ordering, and same-role events
// that coincide in one run and are 1 ms (or 1 s) apart in the other, give
// the same output.
func TestTemplate_SameOrderingDifferentTimes_SameOutput(t *testing.T) {
	runA := templated(t, `{"x":{"created":1700000000},"y":{"created":1700000000},
		"z":[{"started_at_ms":1700000000100,"ended_at_ms":1700000000200},{"started_at_ms":1700000000250,"ended_at_ms":1700000000300}],
		"e":[{"timestamp_ms":1700000000400},{"timestamp_ms":1700000000400}]}`)
	runB := templated(t, `{"x":{"created":1800000000},"y":{"created":1800000001},
		"z":[{"started_at_ms":1800000001100,"ended_at_ms":1800000001250},{"started_at_ms":1800000001300,"ended_at_ms":1800000001900}],
		"e":[{"timestamp_ms":1800000001950},{"timestamp_ms":1800000001951}]}`)

	if runA != runB {
		t.Fatalf("A %s\nB %s", runA, runB)
	}
}

// (c) ISO timestamps to the second that coincide still get distinct numbers.
func TestTemplate_CoincidentISOSeconds_DistinctPlaceholders(t *testing.T) {
	got := templated(t, `{"a":"at 2026-11-03T10:00:00Z","b":"at 2026-11-03T10:00:00Z","c":"2026-11-03 09:59:59"}`)

	want := `{"a":"at {{timestamp_2}}","b":"at {{timestamp_3}}","c":"{{timestamp_1}}"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestTemplate_SameContentDifferentVolatileValues_SameOutput(t *testing.T) {
	a := templated(t, `{"trace_id":"11111111111111111111111111111111","id":"resp_9f8e7d6c5b","created_at":1700000000}`)
	b := templated(t, `{"trace_id":"22222222222222222222222222222222","id":"resp_1a2b3c4d5e","created_at":1800000000}`)

	if a != b {
		t.Fatalf("a %s\nb %s", a, b)
	}
}

func TestTemplate_NumbersKeepPrecision(t *testing.T) {
	got := templated(t, `{"amount":12345678901234567890,"price":0.1}`)

	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if string(m["amount"]) != "12345678901234567890" || string(m["price"]) != "0.1" {
		t.Fatalf("got %s", got)
	}
}
