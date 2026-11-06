package capture

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/redact"
)

const jsonSecret = `hunter"2-planted-secret-value`

func jsonSecretRedactor() *redact.Redactor {
	return redact.FromEnviron([]string{"PLANTED_API_KEY=" + jsonSecret})
}

// argumentsString returns tool-call arguments as providers send them: a JSON
// object serialized into a string.
func argumentsString(t *testing.T, value string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"password": value})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func jsonText(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func openAIToolCallDoc(t *testing.T, arguments string) []byte {
	t.Helper()
	return []byte(jsonText(t, map[string]any{"choices": []any{map[string]any{"message": map[string]any{
		"tool_calls": []any{map[string]any{"function": map[string]any{"name": "login", "arguments": arguments}}},
	}}}}))
}

func anthropicToolStream(t *testing.T, arguments string) string {
	t.Helper()
	third := len(arguments) / 3
	parts := []string{arguments[:third], arguments[third : 2*third], arguments[2*third:]}
	var b strings.Builder
	for _, p := range parts {
		ev := map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": p}}
		fmt.Fprintf(&b, "event: content_block_delta\ndata: %s\n\n", jsonText(t, ev))
	}
	return b.String()
}

func TestRedactTree_OpenAIFunctionArgumentsWithQuote_Redacted(t *testing.T) {
	doc, err := decodeJSON(openAIToolCallDoc(t, argumentsString(t, jsonSecret)))
	if err != nil {
		t.Fatal(err)
	}

	red, err := redactTree(doc, jsonSecretRedactor())

	if err != nil {
		t.Fatal(err)
	}
	if out := jsonText(t, red); strings.Contains(out, "planted-secret-value") {
		t.Fatalf("function.arguments still holds the secret: %s", out)
	}
}

func TestRedactSSE_AnthropicPartialJSONWithQuote_Redacted(t *testing.T) {
	stream := anthropicToolStream(t, argumentsString(t, jsonSecret))

	out, err := redactSSE(stream, jsonSecretRedactor())

	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "planted-secret-value") {
		t.Fatalf("input_json_delta stream still holds the secret:\n%s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("no redaction marker in:\n%s", out)
	}
}

func TestCheckRedaction_OpenAIFunctionArgumentsWithQuote_HitWithoutSecret(t *testing.T) {
	doc := openAIToolCallDoc(t, argumentsString(t, jsonSecret))

	hits, err := CheckRedaction(doc, jsonSecretRedactor())

	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %v; want one", hits)
	}
	if printed := fmt.Sprint(hits); strings.Contains(printed, "planted-secret-value") {
		t.Fatalf("hit report prints the secret: %s", printed)
	}
}

func TestCheckRedaction_AnthropicPartialJSONWithQuote_HitWithoutSecret(t *testing.T) {
	body := anthropicToolStream(t, argumentsString(t, jsonSecret))
	doc := []byte(jsonText(t, map[string]any{"exchanges": []any{map[string]any{"response": map[string]any{"content_type": "text/event-stream", "status": 200, "body": body}}}}))

	hits, err := CheckRedaction(doc, jsonSecretRedactor())

	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("an unredacted input_json_delta stream was not flagged")
	}
	if printed := fmt.Sprint(hits); strings.Contains(printed, "planted-secret-value") {
		t.Fatalf("hit report prints the secret: %s", printed)
	}
}

func TestRedactTree_ArgumentsWithAsciiEscapedNonASCII_Redacted(t *testing.T) {
	bs := string(rune(92))
	arguments := `{"password": "p` + bs + `u00e4ssw` + bs + `u00f6rd-planted-value"}` // Python json.dumps, ensure_ascii
	doc, err := decodeJSON(openAIToolCallDoc(t, arguments))
	if err != nil {
		t.Fatal(err)
	}

	red, err := redactTree(doc, redact.New("pässwörd-planted-value"))

	if err != nil {
		t.Fatal(err)
	}
	if out := jsonText(t, red); strings.Contains(out, "planted-value") {
		t.Fatalf("ASCII-escaped arguments still hold the secret: %s", out)
	}
}
