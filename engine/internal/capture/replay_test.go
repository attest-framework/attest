package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/redact"
)

// deadUpstreams point at a closed port: any request the replay proxy forwards
// fails, so a passing test made no live call.
const liveKey = "sk-live-test-key-0123456789"

func deadUpstreams() Upstreams {
	return Upstreams{OpenAI: "http://127.0.0.1:1/v1", Anthropic: "http://127.0.0.1:1"}
}

func startReplay(t *testing.T, f *Fixture, mode string, environ []string, up Upstreams) *Session {
	t.Helper()
	s, err := StartReplay(ReplayOptions{Mode: mode, Fixture: f, Environ: environ, Upstreams: up})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func post(t *testing.T, url, body string) (int, http.Header, string) {
	t.Helper()
	return postAs(t, url, body, "")
}

// postAs is post with a bearer credential.
func postAs(t *testing.T, url, body, credential string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(b)
}

// recordedBody is the bytes a client must receive for a recorded response.
func recordedBody(t *testing.T, ex Exchange) string {
	t.Helper()
	if len(ex.Response.Body) > 0 && ex.Response.Body[0] == '"' {
		var s string
		if err := json.Unmarshal(ex.Response.Body, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, ex.Response.Body); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// handFixture builds a replayable fixture from request/response pairs.
func handFixture(t *testing.T, name string, pairs ...[2]string) *Fixture {
	t.Helper()
	f := &Fixture{
		FixtureVersion: FixtureVersion,
		Name:           name,
		Source:         Source{Kind: SourceCommand, ReplayCapable: true, Command: []string{"agent"}},
	}
	for i, p := range pairs {
		f.Exchanges = append(f.Exchanges, Exchange{
			Index: i, Provider: "openai", API: "chat_completions", Method: "POST", Path: "/chat/completions",
			Request:  json.RawMessage(p[0]),
			Response: Response{Status: 200, ContentType: "application/json", Body: json.RawMessage(p[1])},
		})
	}
	return f
}

func chatReq(system, user string, extra string) string {
	return fmt.Sprintf(`{"model":"gpt-test","messages":[{"role":"system","content":%q},{"role":"user","content":%q}]%s}`, system, user, extra)
}

func chatURL(s *Session) string { return s.Env()["OPENAI_BASE_URL"] + "/chat/completions" }

var missPattern = regexp.MustCompile("^replay miss: (.+) changed \\(hash ([0-9a-f]{7}|none)→([0-9a-f]{7}|none)\\); re-record with `attest capture --refresh (\\S+)`$")

func missOf(t *testing.T, body string) string {
	t.Helper()
	var doc struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("miss body %q: %v", body, err)
	}
	if doc.Error.Type != "attest_replay_miss" {
		t.Fatalf("error type %q in %s", doc.Error.Type, body)
	}
	return doc.Error.Message
}

func TestReplay_EveryAPIStreamedAndNot_ServedByteForByte(t *testing.T) {
	// Arrange: capture the scripted run, then replay it with the upstream dead.
	up := newFakeUpstream(t)
	fixture, _ := captureRun(t, up, 1, false)
	if len(fixture.Exchanges) != 3 {
		t.Fatalf("exchanges = %d", len(fixture.Exchanges))
	}
	s := startReplay(t, fixture, ReplayStrict, []string{"OPENAI_API_KEY=" + plantedKey}, deadUpstreams())
	env := s.Env()
	tools := `[{"type":"function","function":{"name":"issue_refund","parameters":{"type":"object","properties":{"order_id":{"type":"string"}}}}}]`
	calls := []struct{ url, body string }{
		{env["OPENAI_BASE_URL"] + "/chat/completions", `{"model":"gpt-test","temperature":0,"user":"` + plantedKey + `","tools":` + tools + `,"messages":[{"role":"user","content":"Refund order O-1 for ` + plantedEmail + `"}]}`},
		{env["OPENAI_BASE_URL"] + "/responses", `{"model":"gpt-test","stream":true,"input":"Summarize the refund for ` + plantedEmail + `"}`},
		{env["ANTHROPIC_BASE_URL"] + "/v1/messages", `{"model":"claude-test","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Write the customer note"}]}`},
	}

	// Act and Assert: each response is the recorded one, byte for byte.
	for i, c := range calls {
		status, hdr, body := post(t, c.url, c.body)
		ex := fixture.Exchanges[i]
		if status != ex.Response.Status {
			t.Fatalf("call %d: status %d, recorded %d; body %s", i, status, ex.Response.Status, body)
		}
		if want := recordedBody(t, ex); body != want {
			t.Fatalf("call %d: body differs from recording\n got: %q\nwant: %q", i, body, want)
		}
		if ct := hdr.Get("Content-Type"); ct != ex.Response.ContentType {
			t.Fatalf("call %d: content type %q, recorded %q", i, ct, ex.Response.ContentType)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	sum := s.ReplaySummary()
	if sum.Replayed != 3 || sum.Live != 0 || sum.Misses != 0 || sum.Unused != 0 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestReplay_StreamedResponse_ArrivesAsSeparateEvents(t *testing.T) {
	stream := "data: {\"a\":1}\n\ndata: {\"a\":2}\n\ndata: [DONE]\n\n"
	f := handFixture(t, "stream", [2]string{chatReq("s", "u", `,"stream":true`), quoteJSON(stream)})
	f.Exchanges[0].Response.ContentType = "text/event-stream"
	f.Exchanges[0].Stream = true
	s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())

	_, hdr, body := post(t, chatURL(s), chatReq("s", "u", `,"stream":true`))

	if body != stream {
		t.Fatalf("body = %q", body)
	}
	if hdr.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", hdr.Get("Content-Type"))
	}
	if got := splitEvents(stream); len(got) != 3 || strings.Join(got, "") != stream {
		t.Fatalf("splitEvents = %q", got)
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestReplay_SystemPromptChanged_ExactMissMessage(t *testing.T) {
	f := handFixture(t, "inc-1042", [2]string{chatReq("You refund once.", "refund O-1", ""), `{"ok":1}`})
	s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())

	status, _, body := post(t, chatURL(s), chatReq("You refund twice.", "refund O-1", ""))

	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", status)
	}
	msg := missOf(t, body)
	m := missPattern.FindStringSubmatch(msg)
	if m == nil || m[1] != "system prompt" || m[4] != "inc-1042" || m[2] == m[3] {
		t.Fatalf("miss message = %q", msg)
	}
	want := fmt.Sprintf("replay miss: system prompt changed (hash %s→%s); re-record with `attest capture --refresh inc-1042`", m[2], m[3])
	if msg != want {
		t.Fatalf("miss message = %q, want %q", msg, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if sum := s.ReplaySummary(); sum.Misses != 1 || sum.Replayed != 0 || sum.Records[0].Message != msg {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestReplay_AnthropicSystemChanged_NamedAndErrorShaped(t *testing.T) {
	rec := `{"model":"claude-test","system":"be brief","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
	f := handFixture(t, "n", [2]string{rec, `{"type":"message"}`})
	f.Exchanges[0].Provider, f.Exchanges[0].API, f.Exchanges[0].Path = "anthropic", "messages", "/v1/messages"
	s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())

	status, _, body := post(t, s.Env()["ANTHROPIC_BASE_URL"]+"/v1/messages", strings.Replace(rec, "be brief", "be long", 1))

	var doc struct {
		Type  string `json:"type"`
		Error struct{ Message string }
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil || status != 422 || doc.Type != "error" || !strings.HasPrefix(doc.Error.Message, "replay miss: system prompt changed") {
		t.Fatalf("status %d body %s", status, body)
	}
}

func TestReplay_ToolsParamsModelMessagesChanged_EachNamed(t *testing.T) {
	tools := `,"tools":[{"type":"function","function":{"name":"a"}}]`
	base := chatReq("sys", "user", `,"temperature":0`+tools)
	cases := []struct{ name, live, want string }{
		{"tools", chatReq("sys", "user", `,"temperature":0,"tools":[{"type":"function","function":{"name":"b"}}]`), "tools changed"},
		{"tools removed", chatReq("sys", "user", `,"temperature":0`), "tools changed"},
		{"params", chatReq("sys", "user", `,"temperature":1`+tools), "params changed"},
		{"messages", chatReq("sys", "other user", `,"temperature":0`+tools), "messages changed"},
		{"model", strings.Replace(base, "gpt-test", "gpt-other", 1), `model changed ("gpt-test"→"gpt-other")`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := handFixture(t, "fx", [2]string{base, `{"ok":1}`})
			s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())

			_, _, body := post(t, chatURL(s), c.live)

			msg := missOf(t, body)
			if !strings.HasPrefix(msg, "replay miss: "+c.want) || !strings.HasSuffix(msg, "re-record with `attest capture --refresh fx`") {
				t.Fatalf("miss message = %q, want %q", msg, c.want)
			}
		})
	}
}

func TestReplay_IdenticalRetries_ConsumedInOrder(t *testing.T) {
	req := chatReq("sys", "same", "")
	f := handFixture(t, "retry", [2]string{req, `{"n":1}`}, [2]string{req, `{"n":2}`})
	s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())

	_, _, first := post(t, chatURL(s), req)
	_, _, second := post(t, chatURL(s), req)
	status, _, third := post(t, chatURL(s), req)

	if first != `{"n":1}` || second != `{"n":2}` {
		t.Fatalf("responses = %s, %s", first, second)
	}
	if status != 422 || !strings.Contains(missOf(t, third), "already replayed") {
		t.Fatalf("third call: %d %s", status, third)
	}
}

func TestReplay_EchoedPlaceholderIDs_MatchRecording(t *testing.T) {
	// Recorded request 2 carries the tool call id under document-wide number
	// 3; the live agent echoes the placeholder the replayed response held
	// ({{id_1}}), or a raw id when the run is live.
	tool := func(id string) string {
		return fmt.Sprintf(`{"model":"gpt-test","messages":[{"role":"user","content":"go"},{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":%q,"content":"ok"}]}`, id, id)
	}
	f := handFixture(t, "echo", [2]string{tool("call_{{id_3}}"), `{"done":true}`})
	for _, id := range []string{"call_{{id_1}}", "call_abc123XYZ789"} {
		t.Run(id, func(t *testing.T) {
			s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())

			status, _, body := post(t, chatURL(s), tool(id))

			if status != 200 || body != `{"done":true}` {
				t.Fatalf("%d %s", status, body)
			}
		})
	}
}

func TestReplay_TimestampsAndEmails_NormalizedPerRequest(t *testing.T) {
	rec := `{"model":"gpt-test","messages":[{"role":"user","content":"mail {{email_1}} at {{timestamp_2}}"}]}`
	f := handFixture(t, "vol", [2]string{rec, `{"ok":1}`})
	s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())

	status, _, body := post(t, chatURL(s), `{"model":"gpt-test","messages":[{"role":"user","content":"mail bob@example.org at 2026-11-03T10:11:12Z"}]}`)

	if status != 200 {
		t.Fatalf("%d %s", status, body)
	}
}

func TestReplay_LiveSecretInRequest_RedactedBeforeMatching(t *testing.T) {
	secret := "hunter2-planted-value-xyz"
	rec := `{"model":"gpt-test","messages":[{"role":"user","content":"token ` + redact.Marker + `"}]}`
	f := handFixture(t, "sec", [2]string{rec, `{"ok":1}`})
	live := `{"model":"gpt-test","messages":[{"role":"user","content":"token ` + secret + `"}]}`

	withSecret := startReplay(t, f, ReplayStrict, []string{"SERVICE_TOKEN=" + secret}, deadUpstreams())
	status, _, body := post(t, chatURL(withSecret), live)
	if status != 200 {
		t.Fatalf("redacted secret did not match: %d %s", status, body)
	}

	without := startReplay(t, f, ReplayStrict, nil, deadUpstreams())
	if status, _, _ := post(t, chatURL(without), live); status != 422 {
		t.Fatalf("unredacted secret matched: %d", status)
	}
}

func TestReplay_AutoMode_NoKey_MissIsNotForwarded(t *testing.T) {
	up := newFakeUpstream(t)
	f := handFixture(t, "auto", [2]string{chatReq("a", "u", ""), `{"ok":1}`})
	s := startReplay(t, f, ReplayAuto, nil, Upstreams{OpenAI: up.OpenAIBase(), Anthropic: up.AnthropicBase()})

	status, _, _ := post(t, chatURL(s), chatReq("b", "u", ""))

	if status != 422 || up.seq.Load() != 0 {
		t.Fatalf("status %d, upstream calls %d", status, up.seq.Load())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if sum := s.ReplaySummary(); sum.Misses != 1 || sum.Live != 0 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestReplay_AutoMode_WithKey_MissGoesLiveAndIsCounted(t *testing.T) {
	up := newFakeUpstream(t)
	f := handFixture(t, "auto", [2]string{chatReq("a", "u", ""), `{"ok":1}`})
	s := startReplay(t, f, ReplayAuto, []string{"OPENAI_API_KEY=" + liveKey}, Upstreams{OpenAI: up.OpenAIBase(), Anthropic: up.AnthropicBase()})

	status, _, body := postAs(t, chatURL(s), chatReq("b", "u", ""), liveKey)
	hitStatus, _, hit := post(t, chatURL(s), chatReq("a", "u", ""))

	if status != 200 || !strings.Contains(body, "hello from chat") {
		t.Fatalf("live call: %d %s", status, body)
	}
	if hitStatus != 200 || hit != `{"ok":1}` {
		t.Fatalf("recorded call: %d %s", hitStatus, hit)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	sum := s.ReplaySummary()
	if sum.Live != 1 || sum.Replayed != 1 || sum.Misses != 0 || up.seq.Load() != 1 {
		t.Fatalf("summary = %+v, upstream calls %d", sum, up.seq.Load())
	}
	if sum.UnpricedLiveCalls != 1 {
		t.Fatalf("unpriced live calls = %d", sum.UnpricedLiveCalls)
	}
}

func TestReplay_ExtraCallBeyondRecording_NamesIt(t *testing.T) {
	f := handFixture(t, "extra", [2]string{chatReq("a", "u", ""), `{"ok":1}`})
	s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())
	post(t, chatURL(s), chatReq("a", "u", ""))

	_, _, body := post(t, chatURL(s), chatReq("a", "v", ""))

	if msg := missOf(t, body); !strings.Contains(msg, "more model calls than the 1 recorded") {
		t.Fatalf("miss message = %q", msg)
	}
}

func TestReplay_UnusedRecordings_Counted(t *testing.T) {
	f := handFixture(t, "unused", [2]string{chatReq("a", "1", ""), `{}`}, [2]string{chatReq("a", "2", ""), `{}`})
	s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())
	post(t, chatURL(s), chatReq("a", "1", ""))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if sum := s.ReplaySummary(); sum.Unused != 1 || sum.Replayed != 1 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestNewReplayer_RejectsUnreplayableFixtures(t *testing.T) {
	good := func() *Fixture { return handFixture(t, "ok", [2]string{chatReq("a", "u", ""), `{}`}) }
	cases := []struct {
		name   string
		mutate func(f *Fixture)
		want   string
	}{
		{"trace only", func(f *Fixture) { f.Source = Source{Kind: SourceOTLPFile} }, "trace-only"},
		{"no exchanges", func(f *Fixture) { f.Exchanges = nil }, "no model exchanges"},
		{"truncated", func(f *Fixture) { f.Exchanges[0].Response.Truncated = true }, "truncated"},
		{"bad status", func(f *Fixture) { f.Exchanges[0].Response.Status = 42 }, "not an HTTP status"},
		{"unknown provider", func(f *Fixture) { f.Exchanges[0].Provider = "acme" }, "not supported"},
		{"empty request", func(f *Fixture) { f.Exchanges[0].Request = nil }, "empty"},
		{"bad name", func(f *Fixture) { f.Name = "../x" }, "name"},
		{"too many", func(f *Fixture) {
			for len(f.Exchanges) <= MaxFixtureExchanges {
				f.Exchanges = append(f.Exchanges, f.Exchanges[0])
			}
		}, "at most"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := good()
			c.mutate(f)
			_, err := NewReplayer(ReplayOptions{Mode: ReplayStrict, Fixture: f})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	if _, err := NewReplayer(ReplayOptions{Mode: "loose", Fixture: good()}); err == nil {
		t.Fatal("mode loose accepted")
	}
}

func TestLoadFixture_OversizedFile_Refused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.attest.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	old := fixtureLimit
	fixtureLimit = 1024
	t.Cleanup(func() { fixtureLimit = old })

	_, err := LoadFixture(path)

	if err == nil || !strings.Contains(err.Error(), "larger than 1024 bytes") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadFixture_ProtocolTestFixtures_ValidLoadInvalidRejectedOrUnreplayable(t *testing.T) {
	for _, path := range fixtureFiles(t, "valid") {
		if _, err := LoadFixture(path); err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
		}
	}
	// Invalid fixtures that break an invariant the decoder checks must not
	// load; schema-only violations are caught by the schema tests.
	for _, name := range []string{"005-unknown-top-level-field", "010-fixture-version-2"} {
		if _, err := LoadFixture(filepath.Join("../../../protocol-tests/fixture-v1/invalid", name+".attest.json")); err == nil {
			t.Errorf("%s loaded", name)
		}
	}
}

func TestLoadFixture_MalformedInputs_ErrorNotPanic(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"empty":     "",
		"not-json":  "{",
		"array":     "[]",
		"no-trace":  `{"fixture_version":1}`,
		"trailing":  `{"fixture_version":1} {}`,
		"bad-index": `{"fixture_version":1,"trace":{},"exchanges":[{"index":3}]}`,
	} {
		path := filepath.Join(dir, name+".attest.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFixture(path); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func FuzzReplayLoad(f *testing.F) {
	paths, _ := filepath.Glob("../../../protocol-tests/fixture-v1/*/*.attest.json")
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte(`{"fixture_version":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		fx, err := Unmarshal(data)
		if err != nil {
			return
		}
		// Whatever decodes must be accepted or refused by replay without a
		// panic, and an accepted fixture must answer a request.
		rp, err := NewReplayer(ReplayOptions{Mode: ReplayStrict, Fixture: fx})
		if err != nil {
			return
		}
		rp.decide("openai", http.MethodPost, "/chat/completions", "", []byte(`{"model":"m","messages":[]}`))
	})
}

func TestReplay_ClientSeesNoRealUpstream_HeadersAndHostGuardsStillApply(t *testing.T) {
	f := handFixture(t, "guard", [2]string{chatReq("a", "u", ""), `{}`})
	s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())
	req := httptest.NewRequest(http.MethodPost, chatURL(s), strings.NewReader(chatReq("a", "u", "")))
	req.RequestURI = ""
	req.Host = "evil.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-loopback host: status %d", resp.StatusCode)
	}
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestReplay_UnchangedSystemPrompt_NotBlamedForVolatileValuesInMessages(t *testing.T) {
	// Volatile values are renumbered across the whole request, so a timestamp
	// in a message must not shift the numbers inside an unchanged system prompt.
	rec := `{"model":"gpt-test","messages":[{"role":"system","content":"Now is {{timestamp_1}}"},{"role":"user","content":"hi"}]}`
	live := `{"model":"gpt-test","messages":[{"role":"system","content":"Now is 2026-01-01T00:00:00Z"},{"role":"user","content":"hi at 2026-01-02T00:00:00Z"}]}`
	s := startReplay(t, handFixture(t, "vol", [2]string{rec, `{}`}), ReplayStrict, nil, deadUpstreams())

	_, _, body := post(t, chatURL(s), live)

	if msg := missOf(t, body); !strings.HasPrefix(msg, "replay miss: messages changed") {
		t.Fatalf("miss message = %q", msg)
	}
}

func TestReplay_OtherAPIWithIDInPathAndSecretQuery_Matches(t *testing.T) {
	f := handFixture(t, "other", [2]string{`""`, `{"status":"completed"}`})
	ex := &f.Exchanges[0]
	ex.API, ex.Method, ex.Path = "other", http.MethodGet, "/responses/resp_{{id_2}}?key="+redact.Marker
	for name, livePath := range map[string]string{
		"raw id":              "/responses/resp_abc123xyz789?key=sk-live-secret-0123456789",
		"escaped placeholder": "/responses/resp_%7B%7Bid_1%7D%7D?key=sk-live-secret-0123456789",
	} {
		t.Run(name, func(t *testing.T) {
			s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())

			status, body := get(t, s.Env()["OPENAI_BASE_URL"]+livePath)

			if status != 200 || body != `{"status":"completed"}` {
				t.Fatalf("%d %s", status, body)
			}
		})
	}
}

func TestReplay_AutoMode_JudgeKeyAloneDoesNotGoLive(t *testing.T) {
	up := newFakeUpstream(t)
	f := handFixture(t, "auto", [2]string{chatReq("a", "u", ""), `{}`})
	s := startReplay(t, f, ReplayAuto, []string{"ATTEST_JUDGE_API_KEY=judge-key-0123456789"}, Upstreams{OpenAI: up.OpenAIBase(), Anthropic: up.AnthropicBase()})

	status, _, _ := post(t, chatURL(s), chatReq("b", "u", ""))

	if status != 422 || up.seq.Load() != 0 {
		t.Fatalf("status %d, upstream calls %d", status, up.seq.Load())
	}
}

func TestReplay_LiveRequestRefused_RecordedAsMissNotLive(t *testing.T) {
	up := newFakeUpstream(t)
	f := handFixture(t, "auto", [2]string{chatReq("a", "u", ""), `{}`})
	s := startReplay(t, f, ReplayAuto, []string{"OPENAI_API_KEY=" + liveKey}, Upstreams{OpenAI: up.OpenAIBase(), Anthropic: up.AnthropicBase()})
	s.proxy.budget.charge(MaxSessionBytes + 1)

	status, _, _ := postAs(t, chatURL(s), chatReq("b", "u", ""), liveKey)

	if status != http.StatusInsufficientStorage {
		t.Fatalf("status = %d", status)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if sum := s.ReplaySummary(); sum.Live != 0 || sum.Misses != 1 || !strings.Contains(sum.Records[0].Message, "live request refused") {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestReplay_AutoMode_CredentialNotTheEnvironmentKey_IsNotForwarded(t *testing.T) {
	up := newFakeUpstream(t)
	f := handFixture(t, "auto", [2]string{chatReq("a", "u", ""), `{}`})
	s := startReplay(t, f, ReplayAuto, []string{"OPENAI_API_KEY=" + liveKey}, Upstreams{OpenAI: up.OpenAIBase(), Anthropic: up.AnthropicBase()})

	for name, cred := range map[string]string{"none": "", "other": "sk-attacker-chosen-0123456789"} {
		if status, _, _ := postAs(t, chatURL(s), chatReq("b", "u", ""), cred); status != 422 {
			t.Fatalf("%s: status %d", name, status)
		}
	}
	if up.seq.Load() != 0 {
		t.Fatalf("upstream calls = %d", up.seq.Load())
	}
}

func TestReplay_NoLive_AutoMissWithMatchingKeyIsNotForwarded(t *testing.T) {
	up := newFakeUpstream(t)
	f := handFixture(t, "auto", [2]string{chatReq("a", "u", ""), `{}`})
	s, err := StartReplay(ReplayOptions{Mode: ReplayAuto, Fixture: f, NoLive: true, Environ: []string{"OPENAI_API_KEY=" + liveKey}, Upstreams: Upstreams{OpenAI: up.OpenAIBase(), Anthropic: up.AnthropicBase()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	status, _, _ := postAs(t, chatURL(s), chatReq("b", "u", ""), liveKey)

	if status != 422 || up.seq.Load() != 0 {
		t.Fatalf("status %d, upstream calls %d", status, up.seq.Load())
	}
	_ = s.Close()
	if sum := s.ReplaySummary(); sum.Live != 0 || sum.Misses != 1 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestReplay_RawEmailBesideEchoedPlaceholder_MatchesRecording(t *testing.T) {
	// Capture numbers an email by value, so the recording holds {{email_1}}
	// twice; live, the agent still holds the raw address next to the
	// placeholder the replayed reply echoed.
	rec := `{"model":"gpt-test","messages":[{"role":"user","content":"refund for {{email_1}}"},{"role":"assistant","content":"ok {{email_1}}"}]}`
	live := `{"model":"gpt-test","messages":[{"role":"user","content":"refund for bob@example.org"},{"role":"assistant","content":"ok {{email_1}}"}]}`
	s := startReplay(t, handFixture(t, "mail", [2]string{rec, `{"ok":1}`}), ReplayStrict, nil, deadUpstreams())

	status, _, body := post(t, chatURL(s), live)

	if status != 200 {
		t.Fatalf("%d %s", status, body)
	}
}

func TestReplay_ModelChangedWithControlCharacters_QuotedInMissMessage(t *testing.T) {
	f := handFixture(t, "fx", [2]string{chatReq("a", "u", ""), `{}`})
	s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())
	live := strings.Replace(chatReq("a", "u", ""), "gpt-test", `x\u001b[31m\n::error::boom`, 1)

	_, _, body := post(t, chatURL(s), live)

	msg := missOf(t, body)
	if strings.ContainsAny(msg, "\x1b\n") || !strings.Contains(msg, `\u001b`) && !strings.Contains(msg, `\x1b`) {
		t.Fatalf("miss message = %q", msg)
	}
}

const (
	nonceA = "0123456789abcdef0123456789abcdef"
	nonceB = "fedcba9876543210fedcba9876543210"
)

func judgeSystem(nonce, rule string) string {
	return fmt.Sprintf("Judge the answer. %s <<<AGENT_OUTPUT_START%s>>> to <<<AGENT_OUTPUT_END%s>>>.", rule, nonce, nonce)
}

func judgeUser(nonce, output string) string {
	return fmt.Sprintf("<<<AGENT_OUTPUT_START%[1]s>>>\n%[2]s\n<<<AGENT_OUTPUT_END%[1]s>>>", nonce, output)
}

func TestReplay_JudgeRequestWithFreshNonce_MatchesRecording(t *testing.T) {
	// A recording made with fixed delimiters and one made with a nonce both
	// replay a request that carries a different nonce.
	for name, recorded := range map[string]string{"fixed": "", "nonce": ":" + nonceA} {
		t.Run(name, func(t *testing.T) {
			f := handFixture(t, "judge", [2]string{chatReq(judgeSystem(recorded, "Score it."), judgeUser(recorded, "ok"), ""), `{"ok":1}`})
			s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())

			status, _, body := post(t, chatURL(s), chatReq(judgeSystem(":"+nonceB, "Score it."), judgeUser(":"+nonceB, "ok"), ""))

			if status != http.StatusOK || body != `{"ok":1}` {
				t.Fatalf("status=%d body=%s, want the recorded reply", status, body)
			}
		})
	}
}

func TestReplay_JudgeRequestWithChangedRubricOrOutput_StillMisses(t *testing.T) {
	f := handFixture(t, "judge", [2]string{chatReq(judgeSystem("", "Score it."), judgeUser("", "ok"), ""), `{"ok":1}`})
	cases := map[string]struct{ system, user, component string }{
		"rubric changed": {judgeSystem(":"+nonceB, "Score it harshly."), judgeUser(":"+nonceB, "ok"), "system prompt"},
		"output changed": {judgeSystem(":"+nonceB, "Score it."), judgeUser(":"+nonceB, "different"), "messages"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := startReplay(t, f, ReplayStrict, nil, deadUpstreams())

			status, _, body := post(t, chatURL(s), chatReq(c.system, c.user, ""))

			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want a replay miss", status)
			}
			if m := missPattern.FindStringSubmatch(missOf(t, body)); m == nil || m[1] != c.component {
				t.Fatalf("miss = %q, want %q changed", body, c.component)
			}
		})
	}
}
