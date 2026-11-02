package redact

import (
	"strings"
	"testing"
)

func TestIsSecretEnvName(t *testing.T) {
	for name, want := range map[string]bool{
		"OPENAI_API_KEY": true, "GITHUB_TOKEN": true, "client_secret": true, "DB_PASSWORD": true,
		"PATH": false, "OPENAI_BASE_URL": false, "KEYBOARD": false,
	} {
		if got := IsSecretEnvName(name); got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
}

func TestIsSecretHeader_CaseInsensitive(t *testing.T) {
	for _, h := range []string{"Authorization", "x-api-key", "API-Key", "OpenAI-Organization", "openai-project"} {
		if !IsSecretHeader(h) {
			t.Errorf("%s: want secret", h)
		}
	}
	if IsSecretHeader("Content-Type") {
		t.Error("Content-Type: want not secret")
	}
}

func TestFromEnviron_RedactsSecretValuesOnly(t *testing.T) {
	r := FromEnviron([]string{"MY_API_KEY=planted-value-123", "HOME=/home/someone", "SHORT_TOKEN=abc"})

	got := r.String("key planted-value-123 home /home/someone short abc")

	if got != "key [redacted] home /home/someone short abc" {
		t.Fatalf("got %q", got)
	}
}

func TestString_KeyShapedValues_Redacted(t *testing.T) {
	r := New()
	for _, secret := range []string{
		"sk-proj-abcdefghijklmnop1234",
		"sk-ant-api03-abcdefghijklmnopqrstu",
		"AKIAABCDEFGHIJKLMNOP",
		"AIza" + strings.Repeat("a", 35),
		"ghp_" + strings.Repeat("b", 36),
		"github_pat_" + strings.Repeat("c", 22),
		"xoxb-1234567890-abc",
		"Bearer abcdefghijklmnop.qrstu",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----",
	} {
		if got := r.String("x " + secret + " y"); got != "x "+Marker+" y" {
			t.Errorf("%q: got %q", secret, got)
		}
	}
}

func TestString_OrdinaryText_Unchanged(t *testing.T) {
	r := New()
	for _, s := range []string{"task-runner sk-short", "risk-assessment-of-the-order", "call_customer", "Bearer token"} {
		if got := r.String(s); got != s {
			t.Errorf("%q changed to %q", s, got)
		}
	}
}

func TestNew_LongestSecretFirst(t *testing.T) {
	r := New("abcdefgh", "abcdefgh-longer")

	if got := r.String("abcdefgh-longer"); got != Marker {
		t.Fatalf("got %q", got)
	}
}

func TestScan_ReportsOffsetsNotSecrets(t *testing.T) {
	r := New("planted-value-123")

	hits := r.Scan("a planted-value-123 b sk-abcdefghijklmnopqrst")

	if len(hits) != 2 || hits[0].Kind != "env" || hits[0].Offset != 2 || hits[1].Kind != "pattern" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestSecret_ReplacesNamedValues(t *testing.T) {
	if got := Secret("key k1 and k2", "k1", "", "k2"); got != "key [redacted] and [redacted]" {
		t.Fatalf("got %q", got)
	}
}
