// Package providers holds the native LLM judge backends other than the
// OpenAI-compatible provider in package llm.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxResponseBytes caps how much of a provider response body is read.
const maxResponseBytes = 8 << 20

// maxErrorDetail caps how much of an unparseable error body is echoed.
const maxErrorDetail = 512

// minOutputTokens is the smallest output budget sent to Anthropic and
// Gemini. Their current models reason before answering and count that
// reasoning against the output budget, so the judge's short budget (sized
// for the JSON verdict alone) would end the turn before any text.
const minOutputTokens = 4096

// outputBudget returns the request's MaxTokens raised to minOutputTokens.
func outputBudget(requested int) int {
	return max(requested, minOutputTokens)
}

// requestTimeout bounds a single HTTP round trip; the judge's own context
// deadline (ATTEST_JUDGE_TIMEOUT) still applies on top of it.
const requestTimeout = 60 * time.Second

// newHTTPClient returns a client that never follows redirects. Go strips
// only Authorization and cookies on a cross-host redirect, so following one
// would replay x-api-key / x-goog-api-key and the prompt to the redirect
// target. A 3xx therefore surfaces as a non-2xx StatusError.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// StatusError is returned for a non-2xx provider response. It never carries
// request headers, so API keys cannot leak through it.
type StatusError struct {
	Provider   string
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.StatusCode, e.Message)
}

// postRequest is one JSON POST to a provider API.
type postRequest struct {
	provider string
	url      string
	apiKey   string            // redacted from error messages
	headers  map[string]string // including the header carrying apiKey
	body     any
	// errMessage extracts the human-readable message from an error body;
	// it returns "" when the body has no recognizable message.
	errMessage func([]byte) string
}

// postJSON sends r.body as JSON and returns the raw response body for a 2xx
// status. Non-2xx responses become a *StatusError whose message is
// extracted by r.errMessage, has the API key redacted (a gateway may echo
// request headers), is stripped of control and format characters and is
// capped at maxErrorDetail bytes, because it reaches assertion explanations
// and reports.
func postJSON(ctx context.Context, client *http.Client, r postRequest) ([]byte, error) {
	provider := r.provider
	payload, err := json.Marshal(r.body)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", provider, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", provider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: http: %w", provider, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: read body: %w", provider, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := r.errMessage(raw)
		if msg == "" {
			msg = string(raw)
		}
		msg = truncate(redact(stripControl(msg), r.apiKey), maxErrorDetail)
		return nil, &StatusError{Provider: provider, StatusCode: resp.StatusCode, Message: msg}
	}
	return raw, nil
}

// redactedKey replaces an API key found in provider-supplied text.
const redactedKey = "[redacted]"

// redact replaces every occurrence of key in s. An empty key leaves s as is.
func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, redactedKey)
}

// stripControl drops control characters (including terminal escapes) and
// format characters (bidi overrides such as U+202E, isolates, zero-width
// characters) from text supplied by a remote endpoint, so it can neither
// drive a terminal nor visually reorder a report.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// stripTerminalControl drops control characters except newline and tab, and
// format characters (bidi overrides, zero-width), so
// multi-line text from a local agent keeps its layout but cannot carry
// terminal escape sequences into reports.
func stripTerminalControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			return -1
		}
		return r
	}, s)
}

// truncate caps s at n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
