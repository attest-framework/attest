package capture

import (
	"bytes"
	"sort"
	"strings"
	"testing"
)

// chunks splits s into n nearly equal pieces.
func chunks(s string, n int) []string {
	var out []string
	size := (len(s) + n - 1) / n
	for len(s) > 0 {
		k := min(size, len(s))
		out = append(out, s[:k])
		s = s[k:]
	}
	return out
}

func TestCapture_KeyStreamedAcrossDeltas_EveryAPI_AbsentFromFixture(t *testing.T) {
	up := newFakeUpstream(t)
	up.deltas = append(append([]string{"your key is "}, chunks(plantedKey, 6)...), " ok")

	f, data := captureRun(t, up, 1, false)

	if bytes.Contains(data, []byte(plantedKey)) {
		t.Fatal("fixture contains the planted key")
	}
	for _, part := range chunks(plantedKey, 6)[1:5] {
		if bytes.Contains(data, []byte(`"`+part+`"`)) {
			t.Errorf("fixture still holds key fragment %q as a delta", part)
		}
	}
	for _, ex := range f.Exchanges[1:] {
		texts, err := streamTexts(mustString(t, ex.Response.Body))
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(mapValues(texts), "|")
		if !strings.Contains(joined, "your key is [redacted] ok") {
			t.Errorf("%s stream text %q; want the marker in place of the key", ex.API, joined)
		}
	}
	hits, err := CheckRedaction(data, redactorFor(plantedKey))
	if err != nil || len(hits) != 0 {
		t.Fatalf("CheckRedaction = %+v, %v", hits, err)
	}
}

func TestRedactSSE_EachDeltaShape_KeyAcrossDeltasRedacted(t *testing.T) {
	parts := chunks(plantedKey, 5)
	ev := func(json string) string { return "data: " + json + "\n\n" }
	for name, build := range map[string]func(p string) string{
		"chat content": func(p string) string { return ev(`{"choices":[{"index":0,"delta":{"content":"` + p + `"}}]}`) },
		"chat tool arguments": func(p string) string {
			return ev(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"` + p + `"}}]}}]}`)
		},
		"responses delta": func(p string) string {
			return "event: response.output_text.delta\n" + ev(`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"`+p+`"}`)
		},
		"anthropic text": func(p string) string {
			return ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + p + `"}}`)
		},
		"anthropic partial_json": func(p string) string {
			return ev(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"` + p + `"}}`)
		},
	} {
		var body strings.Builder
		for _, p := range parts {
			body.WriteString(build(p))
		}
		body.WriteString("data: [DONE]\n\n")

		out, err := redactSSE(body.String(), redactorFor(plantedKey))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		texts, err := streamTexts(out)
		if err != nil {
			t.Fatalf("%s: re-parse: %v", name, err)
		}
		if got := strings.Join(mapValues(texts), ""); got != "[redacted]" {
			t.Errorf("%s: rebuilt text %q", name, got)
		}
		if strings.Contains(out, parts[2]) || !strings.HasSuffix(out, "data: [DONE]\n\n") {
			t.Errorf("%s: stream %q", name, out)
		}
	}
}

func TestRedactSSE_UnparseableStream_Error(t *testing.T) {
	for _, body := range []string{"data: {not json\n\n", "data: [1,2]\n\n"} {
		if _, err := redactSSE(body, redactorFor(plantedKey)); err == nil {
			t.Errorf("%q: want error", body)
		}
	}
}

func TestBuildFixture_UnparseableStream_NoFixture(t *testing.T) {
	ex := rawExchange{provider: "openai", method: "POST", path: "/chat/completions", request: []byte(`{"stream":true}`),
		status: 200, contentType: "text/event-stream", response: []byte("data: " + plantedKey + "\n\n")}

	_, err := exchangeDoc(ex, redactorFor(plantedKey))

	if err == nil || !strings.Contains(err.Error(), "unparseable event stream, no fixture written") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckRedaction_HandMadeFixtureKeySplitAcrossDeltas_Hit(t *testing.T) {
	var body strings.Builder
	for _, p := range chunks(plantedKey, 4) {
		body.WriteString(`data: {"choices":[{"index":0,"delta":{"content":"` + p + `"}}]}` + "\n\n")
	}
	doc, _ := canonical(map[string]any{"exchanges": []any{map[string]any{
		"response": map[string]any{"content_type": "text/event-stream", "body": body.String()},
	}}})

	hits, err := CheckRedaction(doc, redactorFor(plantedKey))

	if err != nil || len(hits) != 1 || !strings.Contains(hits[0].Path, `stream "choice 0 content"`) {
		t.Fatalf("hits %+v err %v", hits, err)
	}
}

func TestRedactPath_DecodesAndRedactsCredentialParams(t *testing.T) {
	got, err := redactPath("/v1/models%2Fgpt?api_key=abc123&Signature=zz&q=hello%20world&token=t")

	if err != nil || got != "/v1/models/gpt?api_key=[redacted]&Signature=[redacted]&q=hello world&token=[redacted]" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestRedactArgv_SecretFlags(t *testing.T) {
	got := redactArgv([]string{"agent", "--api-key", "v1", "--db-password=v2", "-token", "v3", "--name", "n", "--auth", "--x"})

	want := []string{"agent", "--api-key", "[redacted]", "--db-password=[redacted]", "-token", "[redacted]", "--name", "n", "--auth", "[redacted]"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v", got)
	}
}

func mustString(t *testing.T, raw []byte) string {
	t.Helper()
	v, err := decodeJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("body is not a string: %s", raw)
	}
	return s
}

func mapValues(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}
