package capture

import (
	"encoding/json"
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

func TestTemplate_TimestampsBecomeSequenceNumbers_DurationsZero(t *testing.T) {
	got := templated(t, `{"a":{"created":1700000300,"latency_ms":812},
		"b":[{"started_at_ms":1700000000500,"ended_at_ms":1700000000900},{"started_at_ms":1700000000900}],
		"c":"data: {\"created\": 1700000300, \"n\":1}\n\n","d":"2026-11-03T10:00:00Z"}`)

	for _, want := range []string{
		`"created":1`, `"latency_ms":0`, `"ended_at_ms":2`, `"started_at_ms":3`, `{"started_at_ms":4}`,
		`data: {\"created\":5, \"n\":1}`, `"d":"{{timestamp_1}}"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

// Two runs of one scenario can put two responses in the same second, or a
// span end and the next start in the same millisecond, in one run and not in
// the next. The output must not depend on which.
func TestTemplate_CoincidentTimestamps_SameOutputAsDistinct(t *testing.T) {
	same := templated(t, `{"x":{"created":1700000000},"y":{"created":1700000000},
		"z":[{"started_at_ms":1700000000100,"ended_at_ms":1700000000200},{"started_at_ms":1700000000200}]}`)
	distinct := templated(t, `{"x":{"created":1700000000},"y":{"created":1700000001},
		"z":[{"started_at_ms":1700000000100,"ended_at_ms":1700000000200},{"started_at_ms":1700000000201}]}`)

	if same != distinct {
		t.Fatalf("same     %s\ndistinct %s", same, distinct)
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
