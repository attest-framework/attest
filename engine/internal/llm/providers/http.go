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
	"time"
)

// maxResponseBytes caps how much of a provider response body is read.
const maxResponseBytes = 8 << 20

// maxErrorDetail caps how much of an unparseable error body is echoed.
const maxErrorDetail = 512

// requestTimeout bounds a single HTTP round trip; the judge's own context
// deadline (ATTEST_JUDGE_TIMEOUT) still applies on top of it.
const requestTimeout = 60 * time.Second

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

// postJSON sends body as JSON to url with headers and returns the raw
// response body for a 2xx status. Non-2xx responses become a *StatusError
// whose message is extracted from the body by errMessage.
func postJSON(
	ctx context.Context,
	client *http.Client,
	provider, url string,
	headers map[string]string,
	body any,
	errMessage func([]byte) string,
) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", provider, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", provider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
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
		msg := errMessage(raw)
		if msg == "" {
			msg = truncate(string(raw), maxErrorDetail)
		}
		return nil, &StatusError{Provider: provider, StatusCode: resp.StatusCode, Message: msg}
	}
	return raw, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
