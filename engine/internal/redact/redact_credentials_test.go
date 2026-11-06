package redact

import (
	"strings"
	"testing"
)

func TestRedactor_JWTAndKeyedCredentials_Redacted(t *testing.T) {
	jwt := "eyJ" + strings.Repeat("a", 12) + ".eyJ" + strings.Repeat("b", 12) + "." + strings.Repeat("c", 16)
	azure := strings.Repeat("0123456789abcdef", 2)
	mistral := strings.Repeat("m1", 16)
	cohere := strings.Repeat("c9", 20)
	r := New()
	for name, in := range map[string]string{
		"jwt":     "token " + jwt + " end",
		"azure":   `{"api-key": "` + azure + `"}`,
		"azureq":  "https://x.openai.azure.com/?subscription-key=" + azure,
		"mistral": `MISTRAL_API_KEY=` + mistral,
		"cohere":  `{\"client_secret\":\"` + cohere + `\"}`,
		"access":  "access_key: " + azure,
	} {
		out := r.String(in)
		for _, secret := range []string{jwt, azure, mistral, cohere} {
			if strings.Contains(out, secret) {
				t.Errorf("%s: %q still holds a secret", name, out)
			}
		}
		if !strings.Contains(out, Marker) {
			t.Errorf("%s: no marker in %q", name, out)
		}
	}
}

func TestRedactor_BareHexAndUnrelatedKeys_Kept(t *testing.T) {
	r := New()
	for _, in := range []string{
		`"trace_id": "0123456789abcdef0123456789abcdef"`,
		"sha256 0123456789abcdef0123456789abcdef",
		`"cache_key": "0123456789abcdef0123456789abcdef"`,
	} {
		if out := r.String(in); out != in {
			t.Errorf("%q became %q", in, out)
		}
	}
}

func TestIsSecretHeader_AzureAndAccessKeys(t *testing.T) {
	for _, h := range []string{"Ocp-Apim-Subscription-Key", "subscription-key", "access-key", "client_secret"} {
		if !IsSecretHeader(h) {
			t.Errorf("%s: want secret", h)
		}
	}
}

func TestFromDotenv_SecretNamesAndURLPasswords_Redacted(t *testing.T) {
	r := FromDotenv([]byte("# comment\n\nexport SERVICE_TOKEN='tok-0123456789'\nDB_URL=\"postgres://u:p4ssw0rd-long@h/db\"\nMODE=development-mode\r\nBAD LINE\n"))

	out := r.String("tok-0123456789 p4ssw0rd-long development-mode")

	if out != Marker+" "+Marker+" development-mode" {
		t.Fatalf("out = %q", out)
	}
}
