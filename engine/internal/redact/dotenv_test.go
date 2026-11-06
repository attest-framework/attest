package redact

import (
	"strings"
	"testing"
)

func TestFromDotenv_ValueForms_DecodedValueIsRedactionTarget(t *testing.T) {
	cases := []struct {
		name   string
		dotenv string
		secret string
	}{
		{"unquoted inline comment", "API_TOKEN=abc12345xyz # prod\n", "abc12345xyz"},
		{"unquoted hash without space kept", "API_TOKEN=abc#12345xyz\n", "abc#12345xyz"},
		{"quoted then comment", "API_TOKEN=\"abc12345xyz\" # prod\n", "abc12345xyz"},
		{"single quoted then comment", "API_TOKEN='abc12345xyz' # prod\n", "abc12345xyz"},
		{"double quoted escaped quote", `API_TOKEN="abc\"12345xyz"` + "\n", `abc"12345xyz`},
		{"double quoted escaped backslash", `API_TOKEN="abc\\12345xyz"` + "\n", `abc\12345xyz`},
		{"double quoted newline escape", `API_TOKEN="abc\n12345xyz"` + "\n", "abc\n12345xyz"},
		{"single quoted literal", `API_TOKEN='abc\n12345xyz'` + "\n", `abc\n12345xyz`},
		{"multi-line double quoted", "API_TOKEN=\"abc\n12345xyz\"\nOTHER=1\n", "abc\n12345xyz"},
		{"export prefix", "export API_TOKEN=abc12345xyz\n", "abc12345xyz"},
		{"crlf line endings", "API_TOKEN=abc12345xyz\r\n", "abc12345xyz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := FromDotenv([]byte(c.dotenv))

			got := r.String("leaked " + c.secret + " end")

			if strings.Contains(got, c.secret) {
				t.Fatalf("decoded value still present: %q", got)
			}
		})
	}
}

func TestFromDotenv_CommentTextAfterValue_NotARedactionTarget(t *testing.T) {
	r := FromDotenv([]byte("API_TOKEN=abc12345xyz # prod-comment\n"))

	got := r.String("note prod-comment stays")

	if got != "note prod-comment stays" {
		t.Fatalf("comment text was redacted: %q", got)
	}
}

func TestFromDotenv_MultiLineValue_DoesNotSwallowFollowingVariables(t *testing.T) {
	r := FromDotenv([]byte("A_TOKEN=\"line1abcdef\nline2abcdef\"\nB_TOKEN=zzzz12345678\n"))

	got := r.String("zzzz12345678")

	if got != Marker {
		t.Fatalf("following variable lost: %q", got)
	}
}

func TestRedactor_SecretWithQuote_JSONEscapedFormRedacted(t *testing.T) {
	r := New(`hunter"2-planted-secret-value`)

	got := r.String(`{"q":"hunter\"2-planted-secret-value"}`)

	if strings.Contains(got, "planted-secret") {
		t.Fatalf("JSON-escaped form leaked: %q", got)
	}
}

func TestRedactor_SecretWithBackslash_JSONEscapedFormRedacted(t *testing.T) {
	r := New(`pa\ss-planted-secret-value`)

	got := r.String(`{"q":"pa` + bs + bs + `ss-planted-secret-value"}`)

	if strings.Contains(got, "planted-secret") {
		t.Fatalf("JSON-escaped form leaked: %q", got)
	}
}

// bs is a single backslash, spelled so source tooling cannot decode a \uXXXX escape.
var bs = string(rune(92))

func TestRedactor_NonASCIISecret_AsciiEscapedFormRedacted(t *testing.T) {
	r := New("pässwörd-planted-😀-value")

	got := r.String(`{"q":"p` + bs + `u00e4ssw` + bs + `u00f6rd-planted-` + bs + `ud83d` + bs + `ude00-value"}`)

	if strings.Contains(got, "planted") {
		t.Fatalf("ensure_ascii form leaked: %q", got)
	}
}

func TestRedactor_Scan_JSONEscapedForm_ReportsHit(t *testing.T) {
	r := New(`hunter"2-planted-secret-value`)

	hits := r.Scan(`{"q":"hunter\"2-planted-secret-value"}`)

	if len(hits) != 1 || hits[0].Kind != "env" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestRedactor_MergeTwice_DoesNotDoubleEscapeForms(t *testing.T) {
	r := New(`a"bcdefgh`).Merge(New(`a"bcdefgh`))

	if n := len(r.values); n != 1 {
		t.Fatalf("values = %d, want 1", n)
	}
}

func TestKeyedCredential_LongKey_RedactedWhole(t *testing.T) {
	key := strings.Repeat("aB3dE5gH", 10) + "wxyz" // 84 chars
	r := New()

	got := r.String("api_key=" + key + " tail")

	if got != "api_key="+Marker+" tail" {
		t.Fatalf("got %q", got)
	}
}

func TestFromDotenv_UnterminatedQuote_FollowingVariablesStillTargets(t *testing.T) {
	r := FromDotenv([]byte("A_TOKEN=\"never-closed-value1\nB_TOKEN=zzzz12345678\nC_TOKEN=yyyy87654321\n"))

	got := r.String("zzzz12345678 yyyy87654321")

	if got != Marker+" "+Marker {
		t.Fatalf("variables after an unterminated quote were not targets: %q", got)
	}
}

func TestRedactor_SecretEscapedTwice_RedactedInRawJSONArguments(t *testing.T) {
	r := New(`hunter"2-planted-secret-value`)
	// A tool-call arguments string holds the secret escaped once; that string
	// copied raw into JSON text escapes the backslash and quote again.
	doubly := `{\"q\":\"hunter\\\"2-planted-secret-value\"}`

	got := r.String(doubly)

	if strings.Contains(got, "planted-secret") {
		t.Fatalf("doubly escaped form leaked: %q", got)
	}
}
