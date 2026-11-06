// Package redact removes secrets from text before it is written anywhere: a
// provider error message, a capture fixture, a terminal. It is the engine's
// single redactor.
package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf16"
)

// Marker replaces every redacted secret.
const Marker = "[redacted]"

// minSecretLen is the shortest environment value treated as a secret. Shorter
// values ("1", "true", "dev") would erase ordinary words from the fixture.
const minSecretLen = 8

// secretHeaders are request and response headers that carry credentials.
// Current Anthropic SDKs authenticate with Authorization: Bearer, not only
// x-api-key.
var secretHeaders = map[string]bool{
	"authorization":             true,
	"proxy-authorization":       true,
	"x-api-key":                 true,
	"api-key":                   true,
	"subscription-key":          true,
	"access-key":                true,
	"client-secret":             true,
	"client_secret":             true,
	"ocp-apim-subscription-key": true,
	"openai-organization":       true,
	"openai-project":            true,
	"cookie":                    true,
	"set-cookie":                true,
}

// IsSecretHeader reports whether the header name carries a credential.
func IsSecretHeader(name string) bool {
	return secretHeaders[strings.ToLower(name)]
}

// secretEnvSuffixes mark environment variables whose values are secrets.
// PASS and PWD count only after an underscore (DB_PASS, MYSQL_PWD), so the
// working directory (PWD, OLDPWD) is not one.
var secretEnvSuffixes = []string{
	"KEY", "TOKEN", "SECRET", "PASSWORD", "_PASS", "_PWD", "PASSWD",
	"CREDENTIALS", "CREDENTIAL", "DSN", "AUTH",
}

// notSecretEnvNames name programs or places, not secrets.
var notSecretEnvNames = map[string]bool{"PWD": true, "OLDPWD": true, "ASKPASS": true}

// IsSecretEnvName reports whether an environment variable name marks a secret
// value: its name ends in KEY, TOKEN, SECRET, PASSWORD, _PASS, _PWD, PASSWD,
// CREDENTIALS, CREDENTIAL, DSN or AUTH. PWD, OLDPWD and *_ASKPASS (a helper
// program's path) are not secrets.
func IsSecretEnvName(name string) bool {
	upper := strings.ToUpper(name)
	if notSecretEnvNames[upper] || strings.HasSuffix(upper, "_ASKPASS") {
		return false
	}
	for _, s := range secretEnvSuffixes {
		if strings.HasSuffix(upper, s) {
			return true
		}
	}
	return false
}

// secretArgWords mark command-line flags whose value is a secret.
var secretArgWords = []string{"key", "token", "secret", "password", "pass", "auth", "credential"}

// IsSecretArgName reports whether a flag name (without dashes) names a secret.
func IsSecretArgName(name string) bool {
	lower := strings.ToLower(name)
	for _, w := range secretArgWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// keyPatterns match credentials by their published prefixes, whatever
// variable they came from. Each match is redacted whole.
var keyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),                                         // OpenAI, Anthropic (sk-ant-), project keys
	regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`),                      // Stripe
	regexp.MustCompile(`\bgsk_[A-Za-z0-9]{20,}`),                                          // Groq
	regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}`),                                           // Hugging Face
	regexp.MustCompile(`\bxai-[A-Za-z0-9]{20,}`),                                          // xAI
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                            // AWS access key id
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),                                         // Google API key
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`),                                    // GitHub tokens (ghp_, gho_, ghs_, ...)
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`),                                  // GitHub fine-grained token
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),                                 // Slack tokens
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), // JWT
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`),                          // bearer credentials in text
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
}

// contextPatterns match credentials that have no published prefix (Azure's
// 32-hex keys, Mistral's 32-character and Cohere's 40-character keys) by the
// name that introduces them, in a query string, a header dump, a JSON object
// (quotes and backslashes allowed between name and value) or prose. Only the
// value (group 1) is redacted. A bare 32-hex string is not a secret: trace
// ids and content hashes look the same.
var contextPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:api[-_ ]?key|subscription[-_ ]?key|access[-_ ]?key|client[-_ ]?secret)(?:\s+is)?[\s"'\\:=]{1,8}([A-Za-z0-9]{32,})`),
}

// userinfoPattern matches scheme://user:password@ anywhere in text; only the
// password (group 2) is redacted. Neither part may hold whitespace, a double
// quote, a backslash, angle brackets or braces, so a match never crosses a
// JSON string boundary; the user also stops at ':' and the password at '/'
// and '@'. Commas and apostrophes are allowed, as RFC 3986 permits them in
// userinfo.
var userinfoPattern = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://([^\s"\\<>{}/:@]*):([^\s"\\<>{}/@]+)@`)

// Range is a half-open byte range [Start, End) of a secret in a string.
type Range struct {
	Start, End int
	Kind       string // "env" or "pattern"
}

// Redactor replaces known secret values and key-shaped strings.
type Redactor struct {
	values  []string // secrets as given
	targets []string // values plus their JSON-escaped forms
}

// New returns a Redactor for the given literal secrets. Values shorter than
// eight bytes are ignored.
func New(values ...string) *Redactor {
	r := &Redactor{}
	return r.With(values...)
}

// With returns a Redactor holding r's secrets and the given ones.
func (r *Redactor) With(values ...string) *Redactor {
	seen := map[string]bool{}
	out := &Redactor{}
	for _, v := range append(append([]string(nil), r.values...), values...) {
		if len(v) < minSecretLen || seen[v] {
			continue
		}
		seen[v] = true
		out.values = append(out.values, v)
	}
	seenTarget := map[string]bool{}
	for _, v := range out.values {
		for _, t := range append([]string{v}, jsonForms(v)...) {
			if !seenTarget[t] {
				seenTarget[t] = true
				out.targets = append(out.targets, t)
			}
		}
	}
	return out
}

// jsonForms returns the forms a secret takes inside a JSON string, which is
// how tool-call arguments reach a fixture (JSON text inside a string): standard
// escaping, escaping with <, > and & as \uXXXX, and ASCII-only escaping as
// Python's json.dumps writes it. Forms equal to v are omitted.
func jsonForms(v string) []string {
	var forms []string
	add := func(f string) {
		if f != v && !slices.Contains(forms, f) {
			forms = append(forms, f)
		}
	}
	add(jsonEscape(v, false))
	add(jsonEscape(v, true))
	add(asciiEscape(jsonEscape(v, false)))
	add(asciiEscape(jsonEscape(v, true)))
	return forms
}

// jsonEscape returns v as the inside of a JSON string literal.
func jsonEscape(v string, escapeHTML bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(v); err != nil {
		return v // a Go string always encodes; keep the literal form
	}
	return strings.TrimSuffix(strings.TrimSpace(buf.String()), "\"")[1:]
}

// asciiEscape rewrites every non-ASCII rune of an encoded JSON string body as
// \uXXXX, using surrogate pairs above the BMP.
func asciiEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r >= 0x10000:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&b, "\\u%04x\\u%04x", hi, lo)
		default:
			fmt.Fprintf(&b, "\\u%04x", r)
		}
	}
	return b.String()
}

// Merge returns a Redactor holding the secrets of r and o.
func (r *Redactor) Merge(o *Redactor) *Redactor { return r.With(o.values...) }

// secretsOf returns the secret parts of an environment entry: the whole value
// when the name marks a secret, and the password of any URL value with
// userinfo.
func secretsOf(name, value string) []string {
	var out []string
	if IsSecretEnvName(name) {
		out = append(out, value)
	}
	if u, err := url.Parse(value); err == nil && u.User != nil {
		if p, ok := u.User.Password(); ok && p != "" {
			// Both the decoded password and its percent-encoded form as written.
			out = append(out, p)
			if esc := userinfoPassword(value); esc != "" && esc != p {
				out = append(out, esc)
			}
		}
	}
	return out
}

// userinfoPassword returns the password of scheme://user:password@ in value as
// written (percent-encoded), or "".
func userinfoPassword(value string) string {
	m := userinfoPattern.FindStringSubmatch(value)
	if m == nil {
		return ""
	}
	return m[2]
}

// FromEnviron returns a Redactor holding every secret found in environ
// (KEY=VALUE pairs).
func FromEnviron(environ []string) *Redactor {
	var values []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if ok {
			values = append(values, secretsOf(name, value)...)
		}
	}
	return New(values...)
}

// FromDotenv returns a Redactor holding every secret in a dotenv file's
// contents, by the same rule as the process environment: the value of a
// variable whose name marks a secret, and the password of a URL value. Values
// are parsed as python-dotenv and Node dotenv do: blank lines and # comments
// are skipped, an `export ` prefix is allowed, an unquoted value ends at an
// inline ` #` comment, a double-quoted value decodes \" \\ \n \r \t and may
// span lines, a single-quoted value is literal, and text after a closing
// quote is ignored.
func FromDotenv(data []byte) *Redactor {
	var environ []string
	text := string(data)
	for len(text) > 0 {
		var line string
		line, text = cutLine(text)
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if after, found := strings.CutPrefix(name, "export"); found && strings.TrimSpace(after) != after {
			name = strings.TrimSpace(after)
		}
		var value string
		value, text = dotenvValue(strings.TrimLeft(rest, " \t"), text)
		environ = append(environ, name+"="+value)
	}
	return FromEnviron(environ)
}

// cutLine splits s at the first newline, dropping a trailing carriage return.
func cutLine(s string) (line, rest string) {
	line, rest, _ = strings.Cut(s, "\n")
	return strings.TrimSuffix(line, "\r"), rest
}

// dotenvValue parses the value starting at rest (the text after `=` on the
// current line) and returns it with the text left to parse. A quoted value
// continues onto following lines until its closing quote.
func dotenvValue(rest, remaining string) (value, left string) {
	if rest == "" || (rest[0] != '"' && rest[0] != '\'') {
		return stripInlineComment(rest), remaining
	}
	quote := rest[0]
	body := rest[1:]
	for {
		if end := closingQuote(body, quote); end >= 0 {
			// Text after the closing quote on the same line is a comment or noise.
			if quote == '"' {
				return unescapeDouble(body[:end]), remaining
			}
			return body[:end], remaining
		}
		if remaining == "" {
			// Unterminated: the rest of the file is the value, quote dropped.
			break
		}
		var next string
		next, remaining = cutLine(remaining)
		body += "\n" + next
	}
	return strings.TrimSpace(body), ""
}

// closingQuote returns the index of the first unescaped quote in body (a
// backslash escapes the next byte in double quotes only), or -1.
func closingQuote(body string, quote byte) int {
	for i := 0; i < len(body); i++ {
		switch {
		case body[i] == '\\' && quote == '"':
			i++
		case body[i] == quote:
			return i
		}
	}
	return -1
}

var doubleQuoteEscapes = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n", `\r`, "\r", `\t`, "\t")

func unescapeDouble(s string) string { return doubleQuoteEscapes.Replace(s) }

// stripInlineComment drops a trailing comment: whitespace followed by #.
func stripInlineComment(v string) string {
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			v = v[:i]
			break
		}
	}
	return strings.TrimSpace(v)
}

// FromProcessEnv is FromEnviron(os.Environ()).
func FromProcessEnv() *Redactor { return FromEnviron(os.Environ()) }

// Ranges returns the merged, sorted ranges of every secret in s.
func (r *Redactor) Ranges(s string) []Range {
	var rs []Range
	for _, v := range r.targets {
		for off := 0; ; {
			i := strings.Index(s[off:], v)
			if i < 0 {
				break
			}
			rs = append(rs, Range{Start: off + i, End: off + i + len(v), Kind: "env"})
			off += i + len(v)
		}
	}
	for _, p := range keyPatterns {
		for _, loc := range p.FindAllStringIndex(s, -1) {
			rs = append(rs, Range{Start: loc[0], End: loc[1], Kind: "pattern"})
		}
	}
	for _, p := range contextPatterns {
		for _, loc := range p.FindAllStringSubmatchIndex(s, -1) {
			rs = append(rs, Range{Start: loc[2], End: loc[3], Kind: "pattern"})
		}
	}
	for _, loc := range userinfoPattern.FindAllStringSubmatchIndex(s, -1) {
		rs = append(rs, Range{Start: loc[4], End: loc[5], Kind: "pattern"})
	}
	return merge(rs)
}

func merge(rs []Range) []Range {
	if len(rs) == 0 {
		return nil
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Start != rs[j].Start {
			return rs[i].Start < rs[j].Start
		}
		return rs[i].End > rs[j].End
	})
	out := []Range{rs[0]}
	for _, x := range rs[1:] {
		last := &out[len(out)-1]
		if x.Start <= last.End {
			last.End = max(last.End, x.End)
			continue
		}
		out = append(out, x)
	}
	return out
}

// String returns s with every secret replaced by Marker.
func (r *Redactor) String(s string) string {
	rs := r.Ranges(s)
	if len(rs) == 0 {
		return s
	}
	var b strings.Builder
	prev := 0
	for _, x := range rs {
		b.WriteString(s[prev:x.Start])
		b.WriteString(Marker)
		prev = x.End
	}
	b.WriteString(s[prev:])
	return b.String()
}

// Hit is one secret found by Scan. Kind is "env" or "pattern"; the secret
// itself is never part of a Hit.
type Hit struct {
	Kind   string
	Offset int
}

// Scan reports every secret in s.
func (r *Redactor) Scan(s string) []Hit {
	var hits []Hit
	for _, x := range r.Ranges(s) {
		hits = append(hits, Hit{Kind: x.Kind, Offset: x.Start})
	}
	return hits
}

// Secret returns s with every occurrence of the given literal secrets
// replaced by Marker. Empty secrets are ignored; unlike New, short values are
// honored because the caller names them explicitly.
func Secret(s string, secrets ...string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, Marker)
		}
	}
	return s
}
