// Package redact removes secrets from text before it is written anywhere: a
// provider error message, a capture fixture, a terminal. It is the engine's
// single redactor.
package redact

import (
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
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
	"authorization":       true,
	"proxy-authorization": true,
	"x-api-key":           true,
	"api-key":             true,
	"openai-organization": true,
	"openai-project":      true,
	"cookie":              true,
	"set-cookie":          true,
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
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),                    // OpenAI, Anthropic (sk-ant-), project keys
	regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`), // Stripe
	regexp.MustCompile(`\bgsk_[A-Za-z0-9]{20,}`),                     // Groq
	regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}`),                      // Hugging Face
	regexp.MustCompile(`\bxai-[A-Za-z0-9]{20,}`),                     // xAI
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                       // AWS access key id
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),                    // Google API key
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`),               // GitHub tokens (ghp_, gho_, ghs_, ...)
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`),             // GitHub fine-grained token
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),            // Slack tokens
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`),     // bearer credentials in text
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
}

// userinfoPattern matches scheme://user:password@ anywhere in text; only the
// password (group 2) is redacted. User and password exclude quotes, commas,
// braces, backslashes, angle brackets and whitespace, so a match never
// crosses a JSON string boundary.
var userinfoPattern = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://([^\s/:@"',{}\\<>]*):([^\s/@"',{}\\<>]+)@`)

// Range is a half-open byte range [Start, End) of a secret in a string.
type Range struct {
	Start, End int
	Kind       string // "env" or "pattern"
}

// Redactor replaces known secret values and key-shaped strings.
type Redactor struct {
	values []string
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
	return out
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

// FromProcessEnv is FromEnviron(os.Environ()).
func FromProcessEnv() *Redactor { return FromEnviron(os.Environ()) }

// Ranges returns the merged, sorted ranges of every secret in s.
func (r *Redactor) Ranges(s string) []Range {
	var rs []Range
	for _, v := range r.values {
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
