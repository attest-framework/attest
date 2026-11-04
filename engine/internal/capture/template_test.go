package capture

import (
	"encoding/json"
	"fmt"
	"reflect"
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

// A provider id with no digit (OpenAI call ids are 24 base62 characters, so
// about 1.5% of them have none) is templated when it sits in an id field,
// structured or inside JSON held as text; in free text the digit rule still
// keeps tool names such as call_customer.
func TestTemplate_DigitlessIDsInIDFields_TemplatedSoRecaptureIsStable(t *testing.T) {
	const doc = `{"steps":[{"tool_call_id":"%s","name":"call_customer"},{"tool_call_id":"%s"}],
		"attr":"[{\"parts\":[{\"type\":\"tool_call\",\"id\":\"%s\"},{\"type\":\"tool_call\",\"id\":\"%s\"}]}]",
		"sse":"data: {\"tool_calls\":[{\"id\":\"%s\"}]}","note":"called %s via call_customer"}`
	digitless := templated(t, fmt.Sprintf(doc, "call_fcdafade", "call_AbCdEfGhIjKlMnOpQrStUvWx",
		"call_fcdafade", "call_AbCdEfGhIjKlMnOpQrStUvWx", "call_fcdafade", "call_fcdafade"))
	withDigits := templated(t, fmt.Sprintf(doc, "call_1a2b3c4d", "call_9Z8y7X6w5V4u3T2s1R0qPoNm",
		"call_1a2b3c4d", "call_9Z8y7X6w5V4u3T2s1R0qPoNm", "call_1a2b3c4d", "call_1a2b3c4d"))

	if digitless != withDigits {
		t.Fatalf("digitless %s\nwith digits %s", digitless, withDigits)
	}
	for _, gone := range []string{"fcdafade", "AbCdEfGh"} {
		if strings.Contains(digitless, gone) {
			t.Errorf("id %s left untemplated: %s", gone, digitless)
		}
	}
	if !strings.Contains(digitless, `"name":"call_customer"`) || !strings.Contains(digitless, "via call_customer") {
		t.Errorf("tool name call_customer was templated: %s", digitless)
	}
}

// Ids that appear only inside JSON held as text (an OTel message attribute,
// an event-stream body), with no structured copy, are templated too.
func TestTemplate_DigitlessIDsOnlyInTextHeldJSON_Templated(t *testing.T) {
	for name, doc := range map[string]string{
		"attribute": `{"attr":"[{\"parts\":[{\"type\":\"tool_call\",\"id\":\"call_fcdafade\"}]}]"}`,
		"stream":    `{"body":"data: {\"id\":\"chatcmpl-AbCdEfGhIjKl\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\": \"call_AbCdEfGhIjKlMnOpQrStUvWx\"}]}}]}\n\n"}`,
	} {
		got := templated(t, doc)
		for _, gone := range []string{"fcdafade", "AbCdEfGh"} {
			if strings.Contains(got, gone) {
				t.Errorf("%s: id %s left untemplated: %s", name, gone, got)
			}
		}
		if !strings.Contains(got, "{{id_1}}") {
			t.Errorf("%s: no id placeholder: %s", name, got)
		}
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

func TestTemplaterEmails_SharesNumberingWithTheDocumentAndKeepsOtherValues(t *testing.T) {
	tpl := newTemplater()
	tpl.template(map[string]any{"note": "mail lee@example.com then pat@example.com"})

	got, err := tpl.emails(map[string]any{
		"pat@example.com": "id-9007",
		"rows":            []any{map[string]any{"to": "pat@example.com", "cc": "new@example.com", "at": "2024-01-02T03:04:05Z"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"{{email_2}}": "id-9007",
		"rows":        []any{map[string]any{"to": "{{email_2}}", "cc": "{{email_3}}", "at": "2024-01-02T03:04:05Z"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTemplaterEmails_KeysThatCollideAfterTemplatingAreAnError(t *testing.T) {
	_, err := newTemplater().emails(map[string]any{"a@example.com": 1, "{{email_1}}": 2})
	if err == nil {
		t.Fatal("collision accepted")
	}
}

func TestTemplate_ConversationID_ClockDerivedValuesGiveSameOutput(t *testing.T) {
	doc := `{"trace":{"conversation_id":"conv-%d","steps":[]},"spans":[{"attributes":{"gen_ai.conversation.id":"conv-%d"}}]}`

	a := templated(t, fmt.Sprintf(doc, 1762300000111, 1762300000111))
	b := templated(t, fmt.Sprintf(doc, 1762300999222, 1762300999222))

	if a != b {
		t.Fatalf("clock-derived conversation ids leak:\n%s\n%s", a, b)
	}
	if strings.Count(a, "{{conversation_1}}") != 2 || strings.Contains(a, "conv-") {
		t.Fatalf("same id must map to one placeholder: %s", a)
	}
}
