package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/redact"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const (
	ccDeviceID  = "8dd36770ba8b2aa2068271a3ddd8ca779408d1b0831792361f0823128c958601"
	ccAccount   = "5d3c8b1e-2f4a-4c6d-9e7f-0a1b2c3d4e5f"
	ccSession   = "04b5d25a-f148-484e-b2c9-20d0e7a917d9"
	ccNewSess   = "9a8b7c6d-1111-4222-8333-444455556666"
	ccReminder1 = "<system-reminder>\n# Environment\nYou have been invoked in the following environment: \n - Primary working directory: %s\n - Platform: darwin\n</system-reminder>"
	ccReminder2 = "<system-reminder>\nToday's date is %s.\n</system-reminder>\n"
)

func claudeCodeRequest(session, cwd, date string) string {
	userID, _ := json.Marshal(map[string]string{"device_id": ccDeviceID, "account_uuid": ccAccount, "session_id": session})
	req := map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 32000,
		"stream":     true,
		"metadata":   map[string]any{"user_id": string(userID)},
		"system":     []any{map[string]any{"type": "text", "text": "You are Claude Code."}},
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": fmt.Sprintf(ccReminder1, cwd)},
			map[string]any{"type": "text", "text": fmt.Sprintf(ccReminder2, date)},
			map[string]any{"type": "text", "text": "list the refunds"},
		}}},
	}
	b, _ := json.Marshal(req)
	return string(b)
}

const claudeCodeStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_01ABCDEFGH12345\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func anthropicUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, claudeCodeStream)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func captureClaudeCode(t *testing.T, up *httptest.Server) (*Fixture, []byte) {
	t.Helper()
	return captureClaudeCodeAs(t, up, ccSession)
}

func captureClaudeCodeAs(t *testing.T, up *httptest.Server, session string) (*Fixture, []byte) {
	t.Helper()
	s, err := Start(Upstreams{OpenAI: "http://127.0.0.1:1/v1", Anthropic: up.URL})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := post(t, s.Env()["ANTHROPIC_BASE_URL"]+"/v1/messages", claudeCodeRequest(session, "/private/tmp/run-a", "2026-11-04")); code != 200 {
		t.Fatalf("status %d", code)
	}
	traceID, root := randHex(16), randHex(8)
	postJSON(t, s.Env()["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"], []*tracepb.Span{
		span(traceID, root, "", "invoke_agent claude", 1, 2, kv("gen_ai.operation.name", "invoke_agent"), kv("gen_ai.agent.name", "claude")),
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	spans, exchanges, incomplete, err := s.Recorded()
	if err != nil || incomplete != 0 {
		t.Fatalf("recorded: %v, incomplete %d", err, incomplete)
	}
	f, err := BuildFixture(BuildInput{
		Name:      "claude-code",
		Source:    Source{Kind: SourceCommand, ReplayCapable: true, Command: []string{"agent"}},
		Spans:     spans,
		Exchanges: exchanges,
		Redactor:  redactorFor(),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return f, data
}

func TestCapture_ClaudeCodeUserID_NoIdentifierInFixture(t *testing.T) {
	_, data := captureClaudeCode(t, anthropicUpstream(t))

	for name, v := range map[string]string{"device_id": ccDeviceID, "account_uuid": ccAccount, "session_id": ccSession} {
		if bytes.Contains(data, []byte(v)) {
			t.Errorf("fixture still holds the %s value", name)
		}
	}
}

func TestCapture_ClaudeCodeTwoSessions_ByteIdenticalFixtures(t *testing.T) {
	up := anthropicUpstream(t)

	_, first := captureClaudeCodeAs(t, up, ccSession)
	_, second := captureClaudeCodeAs(t, up, ccNewSess)

	if !bytes.Equal(first, second) {
		t.Fatalf("fixtures differ between sessions:\n--- first\n%s\n--- second\n%s", first, second)
	}
}

func TestReplay_ClaudeCodeNewSessionAndEnvironment_Hits(t *testing.T) {
	f, _ := captureClaudeCode(t, anthropicUpstream(t))
	s := startReplay(t, f, "strict", nil, deadUpstreams())

	code, _, body := post(t, s.Env()["ANTHROPIC_BASE_URL"]+"/v1/messages", claudeCodeRequest(ccNewSess, "/private/tmp/run-b", "2026-11-05"))

	if code != 200 || !strings.Contains(body, "message_stop") {
		t.Fatalf("replay with a new session id = %d %q; want the recorded stream", code, body)
	}
}

func TestReplay_ClaudeCodeChangedPrompt_StillMisses(t *testing.T) {
	f, _ := captureClaudeCode(t, anthropicUpstream(t))
	s := startReplay(t, f, "strict", nil, deadUpstreams())

	code, _, _ := post(t, s.Env()["ANTHROPIC_BASE_URL"]+"/v1/messages",
		strings.Replace(claudeCodeRequest(ccNewSess, "/private/tmp/run-b", "2026-11-05"), "list the refunds", "delete the refunds", 1))

	if code == 200 {
		t.Fatal("a changed user prompt replayed a recording")
	}
}

func TestTemplate_LegacyUserID_PartsTemplated(t *testing.T) {
	out := templated(t, `{"metadata":{"user_id":"user_`+ccDeviceID+`_account_`+ccAccount+`_session_`+ccSession+`"}}`)

	for _, v := range []string{ccDeviceID, ccAccount, ccSession} {
		if strings.Contains(out, v) {
			t.Errorf("templated document still holds %s", v)
		}
	}
}

func TestRedactSSE_SecretWithQuoteInResponseCompleted_Redacted(t *testing.T) {
	const secret = `hunter"2-planted-secret-value`
	stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"content\":[{\"text\":\"token is hunter\\\"2-planted-secret-value ok\"}]}]}}\n\n"

	out, err := redactSSE(stream, redact.FromEnviron([]string{"PLANTED_API_KEY=" + secret}))

	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `hunter\"2-planted`) || strings.Contains(out, "planted-secret-value") {
		t.Fatalf("response.completed still holds the secret:\n%s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("no redaction marker in:\n%s", out)
	}
}

func TestRedactSSE_NothingToRedact_ByteIdentical(t *testing.T) {
	stream := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"b\":1,\"a\":\"x\"}}\n\n"

	out, err := redactSSE(stream, redactorFor())

	if err != nil || out != stream {
		t.Fatalf("out = %q, err = %v; want the input unchanged", out, err)
	}
}

func TestCheckRedaction_SecretWithQuoteInSSEBody_HitWithoutSecret(t *testing.T) {
	const secret = `hunter"2-planted-secret-value`
	body := "event: response.completed\ndata: {\"type\":\"response.completed\",\"text\":\"hunter\\\"2-planted-secret-value\"}\n\n"
	doc, _ := json.Marshal(map[string]any{"exchanges": []any{map[string]any{"response": map[string]any{"content_type": "text/event-stream", "status": 200, "body": body}}}})

	hits, err := CheckRedaction(doc, redact.FromEnviron([]string{"PLANTED_API_KEY=" + secret}))

	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("an unredacted event stream was not flagged")
	}
	if printed := fmt.Sprint(hits); strings.Contains(printed, "planted-secret-value") {
		t.Fatalf("hit report prints the secret: %s", printed)
	}
}
