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

func TestIsSecretEnvName_BroadenedSuffixes(t *testing.T) {
	for _, name := range []string{"DB_PASS", "MYSQL_PWD", "FTP_PASSWD", "GOOGLE_APPLICATION_CREDENTIALS",
		"AWS_CREDENTIAL", "SENTRY_DSN", "BASIC_AUTH"} {
		if !IsSecretEnvName(name) {
			t.Errorf("%s: want secret", name)
		}
	}
}

func TestFromEnviron_URLUserinfoPassword_Redacted(t *testing.T) {
	r := FromEnviron([]string{"DATABASE_URL=postgres://app:s3cr%2Ft-pass@db.internal:5432/app"})

	for _, in := range []string{"conn postgres://app:s3cr%2Ft-pass@db", "decoded s3cr/t-pass here"} {
		if got := r.String(in); strings.Contains(got, "s3cr") {
			t.Errorf("%q → %q", in, got)
		}
	}
}

func TestString_NewKeyPrefixes_Redacted(t *testing.T) {
	r := New()
	for _, secret := range []string{
		"sk_live_" + strings.Repeat("a", 24), "sk_test_" + strings.Repeat("b", 24), "rk_live_" + strings.Repeat("c", 24),
		"gsk_" + strings.Repeat("d", 40), "hf_" + strings.Repeat("e", 34), "xai-" + strings.Repeat("f", 40),
		"sk-ant-api03-" + strings.Repeat("g", 30), "gho_" + strings.Repeat("h", 36), "ghs_" + strings.Repeat("i", 36),
		"xoxa-1234567890-abc", "xoxs-1234567890-abc",
	} {
		if got := r.String("x " + secret + " y"); got != "x "+Marker+" y" {
			t.Errorf("%q: got %q", secret, got)
		}
	}
}

func TestString_UserinfoAnywhere_PasswordOnlyRedacted(t *testing.T) {
	got := New().String(`see https://alice:hunter2xyz@example.com/path and redis://:p4ssw0rd@cache:6379`)

	want := `see https://alice:[redacted]@example.com/path and redis://:[redacted]@cache:6379`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestIsSecretArgName(t *testing.T) {
	for name, want := range map[string]bool{
		"api-key": true, "token": true, "client_secret": true, "db-password": true, "pass": true,
		"auth": true, "credentials-file": true, "name": false, "out-dir": false,
	} {
		if got := IsSecretArgName(name); got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
}

func TestRanges_OverlappingSecrets_Merged(t *testing.T) {
	r := New("abcdefgh12345678")
	s := "x sk-abcdefgh12345678abcd y"

	rs := r.Ranges(s)

	if len(rs) != 1 || s[rs[0].Start:rs[0].End] != "sk-abcdefgh12345678abcd" {
		t.Fatalf("ranges %+v", rs)
	}
}
