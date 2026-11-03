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

func TestTemplate_TimestampsNumberedStructurally_DurationsZero(t *testing.T) {
	got := templated(t, `{"a":{"created":1700000300,"latency_ms":812},
		"b":[{"started_at_ms":1700000000900,"ended_at_ms":1700000000500},{"started_at_ms":1700000000100}],
		"c":"data: {\"created\": 1700000300, \"n\":1}\n\n","d":"2023-11-14T22:18:20.950Z"}`)

	// Walk order: a.created, then b[0] start before end whatever the values,
	// b[1] start, c's created, d.
	for _, want := range []string{
		`"created":1`, `{"ended_at_ms":3,"started_at_ms":2}`, `{"started_at_ms":4}`,
		`data: {\"created\":5, \"n\":1}`, `"d":"{{timestamp_6}}"`, `"latency_ms":0`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

// (a) Every object's start precedes its end, zero-length steps and nested
// spans included, and a parent's start and end bracket its children.
func TestTemplate_StartBeforeEnd_StepsNestedSpans_ZeroDuration(t *testing.T) {
	doc := `{"steps":[{"started_at_ms":1700000000100,"ended_at_ms":1700000000300,
			"sub_trace":{"steps":[{"started_at_ms":1700000000150,"ended_at_ms":1700000000150}]}},
		{"started_at_ms":1700000000200,"ended_at_ms":1700000000200}],
		"spans":[{"startTimeUnixNano":"1700000000100000000","endTimeUnixNano":"1700000000100000000",
			"children":[{"startTimeUnixNano":"1700000000100000000","endTimeUnixNano":"1700000000100000000"}]}]}`
	var parsed struct {
		Steps []struct {
			Start json.Number `json:"started_at_ms"`
			End   json.Number `json:"ended_at_ms"`
			Sub   *struct {
				Steps []struct {
					Start json.Number `json:"started_at_ms"`
					End   json.Number `json:"ended_at_ms"`
				} `json:"steps"`
			} `json:"sub_trace"`
		} `json:"steps"`
		Spans []struct {
			Start    string `json:"startTimeUnixNano"`
			End      string `json:"endTimeUnixNano"`
			Children []struct {
				Start string `json:"startTimeUnixNano"`
				End   string `json:"endTimeUnixNano"`
			} `json:"children"`
		} `json:"spans"`
	}
	if err := json.Unmarshal([]byte(templated(t, doc)), &parsed); err != nil {
		t.Fatal(err)
	}
	n := func(v json.Number) int64 { i, _ := v.Int64(); return i }
	a := func(s string) int { i, _ := strconv.Atoi(s); return i }
	for i, s := range parsed.Steps {
		if n(s.Start) >= n(s.End) {
			t.Errorf("step %d: started %s, ended %s", i, s.Start, s.End)
		}
		if s.Sub != nil {
			c := s.Sub.Steps[0]
			if !(n(s.Start) < n(c.Start) && n(c.Start) < n(c.End) && n(c.End) < n(s.End)) {
				t.Errorf("step %d does not bracket its child: %s %s %s %s", i, s.Start, c.Start, c.End, s.End)
			}
		}
	}
	for i, s := range parsed.Spans {
		c := s.Children[0]
		if !(a(s.Start) < a(c.Start) && a(c.Start) < a(c.End) && a(c.End) < a(s.End)) {
			t.Errorf("span %d: %s %s %s %s", i, s.Start, c.Start, c.End, s.End)
		}
	}
}

// (b) Runs that differ only in timing give the same bytes: different absolute
// times, two coincident events vs 1 ms apart, a step end equal to the next
// start vs 1 ms apart, and coincident ISO seconds vs 1 s apart.
func TestTemplate_TimingDifferencesOnly_SameOutput(t *testing.T) {
	runA := templated(t, `{"x":{"created":1700000000},"y":{"created":1700000000},
		"z":[{"started_at_ms":1700000000100,"ended_at_ms":1700000000200},{"started_at_ms":1700000000200,"ended_at_ms":1700000000300}],
		"e":[{"timestamp_ms":1700000000400},{"timestamp_ms":1700000000400}],
		"i":["at 2026-11-03T10:00:00Z","at 2026-11-03T10:00:00Z"]}`)
	runB := templated(t, `{"x":{"created":1800000000},"y":{"created":1800000001},
		"z":[{"started_at_ms":1800000001100,"ended_at_ms":1800000001250},{"started_at_ms":1800000001251,"ended_at_ms":1800000001900}],
		"e":[{"timestamp_ms":1800000001950},{"timestamp_ms":1800000001951}],
		"i":["at 2027-01-01T00:00:00Z","at 2027-01-01T00:00:01Z"]}`)

	if runA != runB {
		t.Fatalf("A %s\nB %s", runA, runB)
	}
	if !strings.Contains(runA, `"i":["at {{timestamp_3}}","at {{timestamp_4}}"]`) {
		t.Fatalf("coincident ISO seconds not numbered apart: %s", runA)
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
