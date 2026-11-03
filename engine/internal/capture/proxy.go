package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Recording limits. A request body above MaxRequestBytes is refused; a
// response body above MaxResponseBytes is still passed to the client but
// recorded only up to the limit and marked truncated.
const (
	MaxRequestBytes  = 32 << 20
	MaxResponseBytes = 32 << 20
	// MaxSessionBytes bounds everything one session records.
	MaxSessionBytes = 512 << 20
	// MaxInFlight bounds concurrent requests on the capture listener.
	MaxInFlight = 64
)

// Default upstreams, used when the capture's environment does not set
// OPENAI_BASE_URL or ANTHROPIC_BASE_URL.
const (
	DefaultOpenAIBaseURL    = "https://api.openai.com/v1"
	DefaultAnthropicBaseURL = "https://api.anthropic.com"
)

// route is one provider prefix on the proxy listener. The client's base URL
// is the listener URL plus prefix; everything after prefix is appended to the
// upstream base URL.
type route struct {
	provider string
	prefix   string
	upstream *url.URL
}

// rawExchange is one proxied exchange before redaction and templating.
type rawExchange struct {
	seq         int
	provider    string
	method      string
	path        string // path after the route prefix, with any query
	request     []byte
	status      int
	contentType string
	response    []byte
	truncated   bool
	done        bool
}

// Proxy forwards provider API calls to their upstream and records each
// exchange. Credentials pass through to the upstream in the request headers;
// the proxy never stores request headers, so they never reach a fixture.
type Proxy struct {
	routes []route
	mu     sync.Mutex
	log    []*rawExchange
	budget *byteBudget
}

// Upstreams holds the provider base URLs the proxy forwards to.
type Upstreams struct {
	OpenAI    string
	Anthropic string
}

// UpstreamsFromEnv reads OPENAI_BASE_URL and ANTHROPIC_BASE_URL from environ
// (KEY=VALUE pairs), falling back to the public endpoints.
func UpstreamsFromEnv(environ []string) Upstreams {
	u := Upstreams{OpenAI: DefaultOpenAIBaseURL, Anthropic: DefaultAnthropicBaseURL}
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case name == "OPENAI_BASE_URL" && value != "":
			u.OpenAI = value
		case name == "ANTHROPIC_BASE_URL" && value != "":
			u.Anthropic = value
		}
	}
	return u
}

// Route prefixes on the capture listener.
const (
	OpenAIPrefix    = "/openai/v1"
	AnthropicPrefix = "/anthropic"
)

// NewProxy returns a recording proxy for the given upstreams.
func NewProxy(up Upstreams) (*Proxy, error) {
	p := &Proxy{budget: &byteBudget{}}
	for _, r := range []struct{ provider, prefix, base string }{
		{"openai", OpenAIPrefix, up.OpenAI},
		{"anthropic", AnthropicPrefix, up.Anthropic},
	} {
		u, err := url.Parse(r.base)
		if err != nil {
			return nil, fmt.Errorf("%s upstream %q: %w", r.provider, r.base, err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%s upstream %q: want an http or https URL", r.provider, r.base)
		}
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawPath = ""
		u.RawQuery = ""
		u.Fragment = ""
		p.routes = append(p.routes, route{provider: r.provider, prefix: r.prefix, upstream: u})
	}
	return p, nil
}

// Register adds the proxy's route prefixes, under base, to mux.
func (p *Proxy) Register(mux *http.ServeMux, base string) {
	for _, r := range p.routes {
		mux.Handle(base+r.prefix+"/", p.forward(r, base+r.prefix))
	}
}

func (p *Proxy) forward(r route, prefix string) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			rest := strings.TrimPrefix(pr.In.URL.Path, prefix)
			pr.Out.URL.Scheme = r.upstream.Scheme
			pr.Out.URL.Host = r.upstream.Host
			pr.Out.URL.Path = r.upstream.Path + rest
			pr.Out.URL.RawPath = ""
			pr.Out.Host = r.upstream.Host
			// Without the client's Accept-Encoding the transport negotiates
			// gzip itself and decodes it, so recordings are plain text.
			pr.Out.Header.Del("Accept-Encoding")
		},
		// -1 flushes every write, so server-sent events reach the client as
		// the upstream produces them.
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			resp.Body = &teeBody{
				rc:          resp.Body,
				ex:          exchangeOf(resp.Request.Context()),
				p:           p,
				status:      resp.StatusCode,
				contentType: resp.Header.Get("Content-Type"),
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				// The client gave up; the exchange stays incomplete and
				// the capture refuses to build a fixture without it.
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			ex := exchangeOf(req.Context())
			body := fmt.Sprintf(`{"error":{"type":"attest_capture_proxy","message":%q}}`, "upstream request failed: "+err.Error())
			p.finish(ex, http.StatusBadGateway, "application/json", []byte(body), false)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, body)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(io.LimitReader(req.Body, MaxRequestBytes+1))
		if err != nil {
			http.Error(w, "attest capture: reading request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body) > MaxRequestBytes {
			http.Error(w, fmt.Sprintf("attest capture: request body exceeds %d bytes", MaxRequestBytes), http.StatusRequestEntityTooLarge)
			return
		}
		// The escaped path, decoded once when the fixture is built.
		path := strings.TrimPrefix(req.URL.EscapedPath(), prefix)
		if req.URL.RawQuery != "" {
			path += "?" + req.URL.RawQuery
		}
		ex, ok := p.begin(r.provider, req.Method, path, body)
		if !ok {
			http.Error(w, fmt.Sprintf("attest capture: the session recorded more than %d bytes", MaxSessionBytes), http.StatusInsufficientStorage)
			return
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		rp.ServeHTTP(w, req.WithContext(withExchange(req.Context(), ex)))
	})
}

func (p *Proxy) begin(provider, method, path string, body []byte) (*rawExchange, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.account(len(body)) {
		return nil, false
	}
	ex := &rawExchange{seq: len(p.log), provider: provider, method: method, path: path, request: body}
	p.log = append(p.log, ex)
	return ex, true
}

// account charges n recorded bytes to the session budget.
func (p *Proxy) account(n int) bool { return p.budget.charge(n) }

func (p *Proxy) finish(ex *rawExchange, status int, contentType string, body []byte, truncated bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ex.done {
		return
	}
	p.account(len(body))
	ex.status, ex.contentType, ex.response, ex.truncated, ex.done = status, contentType, body, truncated, true
}

// Exchanges returns the completed exchanges in request order and the number
// of exchanges whose response body the client never finished reading. A
// session that recorded more than MaxSessionBytes is an error.
func (p *Proxy) Exchanges() ([]rawExchange, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.budget.exceeded() {
		return nil, 0, fmt.Errorf("the session recorded more than %d bytes of model exchanges; no fixture written", MaxSessionBytes)
	}
	out := make([]rawExchange, 0, len(p.log))
	incomplete := 0
	for _, ex := range p.log {
		if !ex.done {
			incomplete++
			continue
		}
		out = append(out, *ex)
	}
	return out, incomplete, nil
}

// teeBody records a response body as the client reads it, so streamed events
// pass through unbuffered. The exchange is complete at EOF.
type teeBody struct {
	rc          io.ReadCloser
	ex          *rawExchange
	p           *Proxy
	status      int
	contentType string
	buf         bytes.Buffer
	truncated   bool
}

func (t *teeBody) Read(b []byte) (int, error) {
	n, err := t.rc.Read(b)
	if n > 0 {
		if room := MaxResponseBytes - t.buf.Len(); room > 0 {
			t.buf.Write(b[:min(n, room)])
			t.truncated = t.truncated || n > room
		} else {
			t.truncated = true
		}
	}
	if err == io.EOF {
		t.complete()
	}
	return n, err
}

// Close ends the body. A body closed before EOF was cut short (the client
// gave up or the upstream failed mid-stream): the exchange stays incomplete,
// and the capture refuses to build a fixture that could not replay it.
func (t *teeBody) Close() error {
	return t.rc.Close()
}

// complete records the exchange. truncated is set only when the response
// exceeded MaxResponseBytes; the client still received all of it.
func (t *teeBody) complete() {
	t.p.finish(t.ex, t.status, t.contentType, bytes.Clone(t.buf.Bytes()), t.truncated)
}

// guard admits only local, non-browser clients, at most MaxInFlight at a
// time. A Host that is not a loopback name (DNS rebinding) and any request
// with an Origin header (a web page) are refused.
func guard(next http.Handler) http.Handler {
	slots := make(chan struct{}, MaxInFlight)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		host := req.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "attest capture: loopback clients only", http.StatusForbidden)
			return
		}
		if req.Header.Get("Origin") != "" {
			http.Error(w, "attest capture: browser requests are refused", http.StatusForbidden)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, fmt.Sprintf("attest capture: more than %d concurrent requests", MaxInFlight), http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, req)
	})
}

type exchangeKey struct{}

func withExchange(ctx context.Context, ex *rawExchange) context.Context {
	return context.WithValue(ctx, exchangeKey{}, ex)
}

func exchangeOf(ctx context.Context) *rawExchange {
	return ctx.Value(exchangeKey{}).(*rawExchange)
}

// shutdownTimeout bounds how long Close waits for in-flight requests.
var shutdownTimeout = 5 * time.Second
