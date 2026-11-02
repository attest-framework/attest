package capture

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/redact"
)

func redactorFor(secrets ...string) *redact.Redactor {
	env := make([]string, len(secrets))
	for i, s := range secrets {
		env[i] = "PLANTED_API_KEY_" + string(rune('A'+i)) + "=" + s
	}
	return redact.FromEnviron(env)
}

func TestCapture_SameScriptedRunTwice_ByteIdenticalFixtures(t *testing.T) {
	up := newFakeUpstream(t)

	_, first := captureRun(t, up, 1, false)
	_, second := captureRun(t, up, 1, false)

	if !bytes.Equal(first, second) {
		t.Fatalf("fixtures differ:\n--- first\n%s\n--- second\n%s", first, second)
	}
}

func TestCapture_PlantedKeyAndEmail_AbsentFromFixture(t *testing.T) {
	up := newFakeUpstream(t)

	_, data := captureRun(t, up, 1, false)

	for _, secret := range []string{plantedKey, plantedEmail, "Bearer"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("fixture contains %q", secret)
		}
	}
	if !bytes.Contains(data, []byte(redact.Marker)) || !bytes.Contains(data, []byte("{{email_1}}")) {
		t.Errorf("fixture lacks the redaction marker or the email placeholder")
	}
	hits, err := CheckRedaction(data, redactorFor(plantedKey))
	if err != nil || len(hits) != 0 {
		t.Fatalf("CheckRedaction = %v, %v; want no hits", hits, err)
	}
	if got := up.auth.Load(); got == nil || !strings.Contains(got.(string), plantedKey) {
		t.Errorf("upstream saw credential %v; the proxy must forward it", got)
	}
}

func TestCapture_EveryAPIStreamedAndNot_RecordedWithDecodedBodies(t *testing.T) {
	up := newFakeUpstream(t)

	f, _ := captureRun(t, up, 1, false)

	want := []struct {
		provider, api, path, text string
		stream                    bool
	}{
		{"openai", "chat_completions", "/chat/completions", "hello from chat", false},
		{"openai", "responses", "/responses", "responses", true},
		{"anthropic", "messages", "/v1/messages", "messages", true},
	}
	if len(f.Exchanges) != len(want) {
		t.Fatalf("got %d exchanges, want %d", len(f.Exchanges), len(want))
	}
	for i, w := range want {
		ex := f.Exchanges[i]
		if ex.Index != i || ex.Provider != w.provider || ex.API != w.api || ex.Path != w.path || ex.Stream != w.stream {
			t.Errorf("exchange %d = %s %s %s stream=%v, want %+v", i, ex.Provider, ex.API, ex.Path, ex.Stream, w)
		}
		if ex.Response.Status != http.StatusOK || ex.Response.Truncated {
			t.Errorf("exchange %d: status %d truncated %v", i, ex.Response.Status, ex.Response.Truncated)
		}
		if !strings.Contains(string(ex.Response.Body), w.text) {
			t.Errorf("exchange %d: body lacks %q (gzip or SSE not decoded): %s", i, w.text, ex.Response.Body)
		}
		if !strings.HasPrefix(ex.Key.PromptHash, "sha256:") {
			t.Errorf("exchange %d: prompt hash %q", i, ex.Key.PromptHash)
		}
	}
	if f.Exchanges[0].Key.ToolSchemaHash == nil || f.Exchanges[1].Key.ToolSchemaHash != nil {
		t.Errorf("tool schema hash: chat %v responses %v; want set, nil", f.Exchanges[0].Key.ToolSchemaHash, f.Exchanges[1].Key.ToolSchemaHash)
	}
	var params map[string]any
	if err := json.Unmarshal(f.Exchanges[0].Key.Params, &params); err != nil || params["temperature"] == nil || params["messages"] != nil {
		t.Errorf("chat params = %s; want temperature without messages", f.Exchanges[0].Key.Params)
	}
	if f.Exchanges[0].Key.Model != "gpt-test" || f.Exchanges[2].Key.Model != "claude-test" {
		t.Errorf("models = %q, %q", f.Exchanges[0].Key.Model, f.Exchanges[2].Key.Model)
	}
}

func TestCapture_DoubleRefund_DraftedCardinalityFails(t *testing.T) {
	up := newFakeUpstream(t)

	plain, _ := captureRun(t, up, 2, false)
	incident, _ := captureRun(t, up, 2, true)

	if plain.Verification.Verified {
		t.Fatalf("double refund verified without --expect-fail: %s", describe(plain))
	}
	if !incident.Verification.Verified {
		t.Fatalf("incident not verified with --expect-fail: %s", describe(incident))
	}
	want := []string{"cardinality-issue_refund-per-order_id"}
	if got := incident.Verification.ExpectedFailures; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("expected failures = %v, want %v (%s)", got, want, describe(incident))
	}
}

func TestCapture_SingleRefund_VerifiedWithoutExpectFail(t *testing.T) {
	up := newFakeUpstream(t)

	f, _ := captureRun(t, up, 1, false)
	incident, _ := captureRun(t, up, 1, true)

	if !f.Verification.Verified {
		t.Fatalf("single refund not verified: %s", describe(f))
	}
	if incident.Verification.Verified {
		t.Fatalf("--expect-fail verified a run where every contract passes")
	}
}

func TestBuildFixture_NoSpans_Errors(t *testing.T) {
	_, err := BuildFixture(BuildInput{Name: "x", Redactor: redact.New()})

	if err == nil || !strings.Contains(err.Error(), "no spans") {
		t.Fatalf("err = %v, want no spans", err)
	}
}

func TestCapture_FixtureRoundTrips_AndPassesFixtureCheck(t *testing.T) {
	up := newFakeUpstream(t)
	_, data := captureRun(t, up, 1, false)

	f, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	report := CheckFixture(data)

	if !bytes.Equal(data, again) {
		t.Errorf("Unmarshal/Marshal changed the fixture")
	}
	if !report.OK || len(report.Issues) != 0 {
		t.Errorf("CheckFixture = %+v", report)
	}
}

func TestSession_NonLoopbackHost_Forbidden(t *testing.T) {
	s, err := Start(Upstreams{OpenAI: "http://127.0.0.1:1/v1", Anthropic: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req, _ := http.NewRequest(http.MethodPost, s.Env()["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"], strings.NewReader("{}"))
	req.Host = "attacker.example"
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

func TestSession_UnreachableUpstream_Returns502AndRecordsIt(t *testing.T) {
	s, err := Start(Upstreams{OpenAI: "http://127.0.0.1:1/v1", Anthropic: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(s.Env()["OPENAI_BASE_URL"]+"/chat/completions", "application/json", strings.NewReader(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, exchanges, _ := s.Recorded()

	if resp.StatusCode != http.StatusBadGateway || len(exchanges) != 1 || exchanges[0].status != http.StatusBadGateway {
		t.Fatalf("status %d, exchanges %+v; want 502 recorded once", resp.StatusCode, exchanges)
	}
}

func TestNewProxy_NonHTTPUpstream_Errors(t *testing.T) {
	_, err := NewProxy(Upstreams{OpenAI: "file:///etc/passwd", Anthropic: DefaultAnthropicBaseURL})

	if err == nil {
		t.Fatal("want an error for a non-http upstream")
	}
}

func TestUpstreamsFromEnv_DefaultsAndOverrides(t *testing.T) {
	got := UpstreamsFromEnv([]string{"OPENAI_BASE_URL=http://localhost:9/v1", "PATH=/bin"})

	if got.OpenAI != "http://localhost:9/v1" || got.Anthropic != DefaultAnthropicBaseURL {
		t.Fatalf("got %+v", got)
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"inc-1042", "a", "Refund.v2_x"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", "-x", strings.Repeat("a", 65)} {
		if ValidateName(bad) == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}
