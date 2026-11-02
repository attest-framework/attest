// Package redact removes secrets from text before it is written anywhere: a
// provider error message, a capture fixture, a terminal. It is the engine's
// single redactor.
package redact

import (
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
var secretEnvSuffixes = []string{"KEY", "TOKEN", "SECRET", "PASSWORD"}

// IsSecretEnvName reports whether an environment variable name marks a secret
// value (its name ends in KEY, TOKEN, SECRET or PASSWORD).
func IsSecretEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, s := range secretEnvSuffixes {
		if strings.HasSuffix(upper, s) {
			return true
		}
	}
	return false
}

// keyPatterns match credentials by their published prefixes, whatever
// variable they came from.
var keyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),                // OpenAI, Anthropic (sk-ant-), project keys
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                   // AWS access key id
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),                // Google API key
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`),           // GitHub tokens
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`),         // GitHub fine-grained token
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),        // Slack tokens
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`), // bearer credentials in text
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
}

// Redactor replaces known secret values and key-shaped strings.
type Redactor struct {
	// values are literal secrets, longest first so a secret that contains
	// another is replaced whole.
	values []string
}

// New returns a Redactor for the given literal secrets. Values shorter than
// eight bytes are ignored.
func New(values ...string) *Redactor {
	seen := map[string]bool{}
	r := &Redactor{}
	for _, v := range values {
		if len(v) < minSecretLen || seen[v] {
			continue
		}
		seen[v] = true
		r.values = append(r.values, v)
	}
	sort.Slice(r.values, func(i, j int) bool {
		if len(r.values[i]) != len(r.values[j]) {
			return len(r.values[i]) > len(r.values[j])
		}
		return r.values[i] < r.values[j]
	})
	return r
}

// FromEnviron returns a Redactor holding the value of every environment
// variable whose name marks a secret.
func FromEnviron(environ []string) *Redactor {
	var values []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if ok && IsSecretEnvName(name) {
			values = append(values, value)
		}
	}
	return New(values...)
}

// FromProcessEnv is FromEnviron(os.Environ()).
func FromProcessEnv() *Redactor { return FromEnviron(os.Environ()) }

// String returns s with every secret value and key-shaped string replaced by
// Marker.
func (r *Redactor) String(s string) string {
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, Marker)
	}
	for _, p := range keyPatterns {
		s = p.ReplaceAllString(s, Marker)
	}
	return s
}

// Hit is one secret found by Scan. Kind is "env" or "pattern"; the secret
// itself is never part of a Hit.
type Hit struct {
	Kind   string
	Offset int
}

// Scan reports every secret value or key-shaped string in s.
func (r *Redactor) Scan(s string) []Hit {
	var hits []Hit
	for _, v := range r.values {
		for off := 0; ; {
			i := strings.Index(s[off:], v)
			if i < 0 {
				break
			}
			hits = append(hits, Hit{Kind: "env", Offset: off + i})
			off += i + len(v)
		}
	}
	for _, p := range keyPatterns {
		for _, loc := range p.FindAllStringIndex(s, -1) {
			hits = append(hits, Hit{Kind: "pattern", Offset: loc[0]})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Offset < hits[j].Offset })
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
