package capture

import (
	"bytes"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/redact"
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
		"chat refusal": func(p string) string { return ev(`{"choices":[{"index":0,"delta":{"refusal":"` + p + `"}}]}`) },
		"chat reasoning_content": func(p string) string {
			return ev(`{"choices":[{"index":0,"delta":{"reasoning_content":"` + p + `"}}]}`)
		},
		"chat function_call": func(p string) string {
			return ev(`{"choices":[{"index":0,"delta":{"function_call":{"arguments":"` + p + `"}}}]}`)
		},
		"chat audio transcript": func(p string) string {
			return ev(`{"choices":[{"index":0,"delta":{"audio":{"transcript":"` + p + `"}}}]}`)
		},
		"responses refusal": func(p string) string {
			return ev(`{"type":"response.refusal.delta","item_id":"m","output_index":0,"content_index":0,"delta":"` + p + `"}`)
		},
		"responses function_call_arguments": func(p string) string {
			return ev(`{"type":"response.function_call_arguments.delta","item_id":"fc","output_index":1,"delta":"` + p + `"}`)
		},
		"responses reasoning_summary_text": func(p string) string {
			return ev(`{"type":"response.reasoning_summary_text.delta","item_id":"rs","output_index":0,"summary_index":0,"delta":"` + p + `"}`)
		},
		"responses reasoning_text": func(p string) string {
			return ev(`{"type":"response.reasoning_text.delta","item_id":"rs","output_index":0,"content_index":0,"delta":"` + p + `"}`)
		},
		"responses audio_transcript": func(p string) string {
			return ev(`{"type":"response.audio_transcript.delta","item_id":"a","output_index":0,"delta":"` + p + `"}`)
		},
		"anthropic thinking": func(p string) string {
			return ev(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"` + p + `"}}`)
		},
		"anthropic citations": func(p string) string {
			return ev(`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"char_location","cited_text":"` + p + `"}}}`)
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

func streamExchange(contentType, body string) rawExchange {
	return rawExchange{provider: "openai", method: "POST", path: "/chat/completions", request: []byte(`{"stream":true}`),
		status: 200, contentType: contentType, response: []byte(body)}
}

func splitKeyStream() string {
	var b strings.Builder
	for _, p := range chunks(plantedKey, 5) {
		b.WriteString(`data: {"choices":[{"index":0,"delta":{"content":"` + p + `"}}]}` + "\n\n")
	}
	return b.String()
}

func TestExchangeDoc_EventStreamWithCharset_Redacted(t *testing.T) {
	d, err := exchangeDoc(streamExchange("text/event-stream; charset=utf-8", splitKeyStream()), redactorFor(plantedKey))
	if err != nil {
		t.Fatal(err)
	}

	body := d["response"].(map[string]any)["body"].(string)
	if strings.Contains(body, chunks(plantedKey, 5)[2]) || !strings.Contains(body, redact.Marker) {
		t.Fatalf("body %q", body)
	}
}

func TestExchangeDoc_StreamRequestedWrongContentType_TreatedAsStream(t *testing.T) {
	d, err := exchangeDoc(streamExchange("application/json", splitKeyStream()), redactorFor(plantedKey))
	if err != nil {
		t.Fatal(err)
	}
	body := d["response"].(map[string]any)["body"].(string)
	if strings.Contains(body, chunks(plantedKey, 5)[2]) {
		t.Fatalf("body %q", body)
	}

	_, err = exchangeDoc(streamExchange("text/plain", "partial "+plantedKey), redactorFor(plantedKey))
	if err == nil || !strings.Contains(err.Error(), "unparseable event stream") {
		t.Fatalf("non-SSE body to a stream request: err %v", err)
	}
}

func TestExchangeDoc_StreamRequestedJSONError_KeptAsJSON(t *testing.T) {
	ex := streamExchange("application/json", `{"error":{"message":"bad key"}}`)
	ex.status = 401

	d, err := exchangeDoc(ex, redactorFor(plantedKey))

	if _, isObj := d["response"].(map[string]any)["body"].(map[string]any); err != nil || !isObj {
		t.Fatalf("err %v doc %v", err, d)
	}
}

// The working directory is not a secret, so the replay key of a prompt that
// mentions a path does not depend on where the capture ran.
func TestBuildFixture_PromptHashStableAcrossWorkingDirectories(t *testing.T) {
	ex := rawExchange{provider: "openai", method: "POST", path: "/chat/completions", status: 200, contentType: "application/json",
		request:  []byte(`{"model":"m","messages":[{"role":"user","content":"read /home/runner/work/app/src/x.py"}]}`),
		response: []byte(`{"choices":[]}`)}
	hash := func(cwd string) string {
		d, err := exchangeDoc(ex, redact.FromEnviron([]string{"PWD=" + cwd}))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := redactTree(d, redact.FromEnviron([]string{"PWD=" + cwd}))
		if err != nil {
			t.Fatal(err)
		}
		req := doc.(map[string]any)["request"]
		if !strings.Contains(mustCanonical(t, req), "/home/runner/work/app/src/x.py") {
			t.Fatalf("path redacted with PWD=%s", cwd)
		}
		k, err := keyOf("chat_completions", req)
		if err != nil {
			t.Fatal(err)
		}
		return k.PromptHash
	}

	if a, b := hash("/home/runner/work/app"), hash("/tmp/elsewhere-dir"); a != b {
		t.Fatalf("prompt hash depends on the working directory: %s vs %s", a, b)
	}
}

func TestRedactArgv_EnvAssignmentsAndShortFlags(t *testing.T) {
	got := redactArgv([]string{"env", "DB_PASSWORD=x1", "PATH=/bin", "API_TOKEN=x2", "cmd", "-p", "short", "--api-key=x3"})

	want := "env DB_PASSWORD=[redacted] PATH=/bin API_TOKEN=[redacted] cmd -p short --api-key=[redacted]"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v", got)
	}
}

func TestRedactPath_LiteralPercent_DecodedOnce(t *testing.T) {
	got, err := redactPath("/files/a%25b%2Fc?q=100%25&token=t%2B1")

	if err != nil || got != "/files/a%b/c?q=100%&token=[redacted]" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestSession_PathWithLiteralPercent_RecordedDecoded(t *testing.T) {
	up := newFakeUpstream(t)
	s, err := Start(Upstreams{OpenAI: up.OpenAIBase(), Anthropic: up.AnthropicBase()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(s.Env()["OPENAI_BASE_URL"]+"/chat/completions?tag=50%25off", "application/json",
		strings.NewReader(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	s.Close()
	_, exchanges, _, err := s.Recorded()
	if err != nil || len(exchanges) != 1 {
		t.Fatalf("exchanges %d err %v", len(exchanges), err)
	}

	d, err := exchangeDoc(exchanges[0], redact.New())

	if err != nil || d["path"] != "/chat/completions?tag=50%off" {
		t.Fatalf("path %v err %v", d["path"], err)
	}
}

func mustCanonical(t *testing.T, v any) string {
	t.Helper()
	b, err := canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A failed stream request gets an error page, not a stream: it is kept as
// text and redacted per string, never refused.
func TestExchangeDoc_StreamRequestedErrorPages_KeptAndRedacted(t *testing.T) {
	for _, c := range []struct {
		status      int
		contentType string
		body        string
	}{
		{502, "text/html", "<html><body>bad gateway for key " + plantedKey + "</body></html>"},
		{429, "text/plain", "rate limited: key " + plantedKey},
	} {
		ex := streamExchange(c.contentType, c.body)
		ex.status = c.status

		d, err := exchangeDoc(ex, redactorFor(plantedKey))
		if err != nil {
			t.Fatalf("%d %s: %v", c.status, c.contentType, err)
		}
		doc, err := redactTree(d, redactorFor(plantedKey))
		if err != nil {
			t.Fatal(err)
		}
		body := doc.(map[string]any)["response"].(map[string]any)["body"].(string)
		if strings.Contains(body, plantedKey) || !strings.Contains(body, redact.Marker) {
			t.Errorf("%d %s: body %q", c.status, c.contentType, body)
		}
	}
}
