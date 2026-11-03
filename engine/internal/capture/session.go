package capture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/attest-ai/attest/engine/internal/redact"
	"github.com/attest-ai/attest/engine/internal/trace"
)

// Session is one capture: a loopback listener hosting the recording proxy
// and the OTLP/HTTP receiver. The agent under test reaches both through the
// environment variables Env returns.
type Session struct {
	proxy     *Proxy
	receiver  *Receiver
	server    *http.Server
	listener  net.Listener
	served    chan error
	closeOnce sync.Once
	closeErr  error
	// token is the first path segment of every capture URL: 128 random
	// bits, so only the process given the environment can reach the
	// listener's routes.
	token string
}

// Start opens the capture listener on an ephemeral 127.0.0.1 port.
// Upstreams are where the proxy forwards model calls.
func Start(up Upstreams) (*Session, error) {
	proxy, err := NewProxy(up)
	if err != nil {
		return nil, err
	}
	return startWith(proxy)
}

// StartReplay starts a session whose proxy answers from a fixture. The
// receiver still runs, so an agent that exports spans does not fail; the
// spans are discarded with the session.
func StartReplay(o ReplayOptions) (*Session, error) {
	rp, err := NewReplayer(o)
	if err != nil {
		return nil, err
	}
	proxy, err := NewProxy(o.Upstreams)
	if err != nil {
		return nil, err
	}
	proxy.replay = rp
	return startWith(proxy)
}

// ReplaySummary reports what a replay session served. It is valid after
// Close, once every in-flight request has finished.
func (s *Session) ReplaySummary() ReplaySummary {
	sum := s.proxy.replay.Summary()
	sum.LiveCostUSD, sum.UnpricedLiveCalls = s.proxy.liveCost()
	return sum
}

func startWith(proxy *Proxy) (*Session, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("capture token: %w", err)
	}
	s := &Session{proxy: proxy, receiver: &Receiver{budget: proxy.budget}, served: make(chan error, 1), token: hex.EncodeToString(raw)}
	mux := http.NewServeMux()
	proxy.Register(mux, "/"+s.token)
	s.receiver.Register(mux, "/"+s.token)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("capture listener: %w", err)
	}
	s.listener = ln
	s.server = &http.Server{
		Handler:           guard(mux),
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout bounds reading a request; there is no WriteTimeout,
		// which would cut long streamed responses.
		ReadTimeout: 60 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	go func() { s.served <- s.server.Serve(ln) }()
	return s, nil
}

// BaseURL is the listener's root URL.
func (s *Session) BaseURL() string { return "http://" + s.listener.Addr().String() }

// Env returns the variables that point an agent at the capture: provider base
// URLs for the proxy and the OTLP traces endpoint for the receiver.
func (s *Session) Env() map[string]string {
	base := s.BaseURL() + "/" + s.token
	return map[string]string{
		"OPENAI_BASE_URL":                    base + OpenAIPrefix,
		"ANTHROPIC_BASE_URL":                 base + AnthropicPrefix,
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": base + TracesPath,
	}
}

// Close stops accepting connections and waits up to shutdownTimeout for
// in-flight requests, then closes the rest: a connection a client opened but
// never used holds Shutdown for its full timeout, and an exchange cut off
// here is incomplete, which fails the capture anyway.
func (s *Session) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.close() })
	return s.closeErr
}

func (s *Session) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := s.server.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		err = s.server.Close()
	}
	if serr := <-s.served; serr != nil && !errors.Is(serr, http.ErrServerClosed) {
		return serr
	}
	return err
}

// Recorded returns what the session captured: spans, completed exchanges
// and the count of exchanges the client never finished reading. A session
// over its recording limit is an error.
func (s *Session) Recorded() ([]trace.Span, []rawExchange, int, error) {
	ex, incomplete, err := s.proxy.Exchanges()
	if err != nil {
		return nil, nil, 0, err
	}
	if inflight, failed := s.receiver.Incomplete(); inflight+failed > 0 {
		return nil, nil, 0, fmt.Errorf("trace export incomplete: %d OTLP export requests unfinished and %d rejected; no fixture written", inflight, failed)
	}
	return s.receiver.Spans(), ex, incomplete, nil
}

// BuildInput is everything a fixture is built from.
type BuildInput struct {
	Name       string
	Source     Source
	Spans      []trace.Span
	Exchanges  []rawExchange
	ExpectFail bool
	AgentID    string
	Redactor   *redact.Redactor
}

// BuildFixture converts the spans to a trace, redacts and templates the trace
// and exchanges together, keys each exchange, drafts contracts and verifies
// them.
func BuildFixture(in BuildInput) (*Fixture, error) {
	if len(in.Spans) == 0 {
		return nil, errors.New("no spans were received; point the agent's OTLP/HTTP exporter at OTEL_EXPORTER_OTLP_TRACES_ENDPOINT and flush it before exit")
	}
	t, err := trace.FromSpans(in.Spans, trace.ConvertOptions{AgentID: in.AgentID})
	if err != nil {
		return nil, err
	}
	report := trace.Check(in.Spans)
	errs := len(report.Errors())
	check := TraceCheckResult{OK: report.OK, Errors: errs, Warnings: len(report.Issues) - errs}

	traceDoc, err := toGeneric(t)
	if err != nil {
		return nil, err
	}
	exDocs := make([]any, 0, len(in.Exchanges))
	for _, ex := range in.Exchanges {
		d, err := exchangeDoc(ex, in.Redactor)
		if err != nil {
			return nil, err
		}
		exDocs = append(exDocs, d)
	}
	command := make([]any, len(in.Source.Command))
	for i, a := range redactArgv(in.Source.Command) {
		command[i] = a
	}
	doc, err := redactTree(map[string]any{"trace": traceDoc, "exchanges": exDocs, "command": command}, in.Redactor)
	if err != nil {
		return nil, err
	}
	doc = Template(doc)
	m := doc.(map[string]any)

	f := &Fixture{
		FixtureVersion: FixtureVersion,
		Name:           in.Name,
		Source:         in.Source,
		Exchanges:      []Exchange{},
	}
	if len(in.Source.Command) > 0 {
		f.Source.Command = make([]string, len(in.Source.Command))
		for i, a := range m["command"].([]any) {
			f.Source.Command[i] = a.(string)
		}
	}
	if err := fromGeneric(m["trace"], &f.Trace); err != nil {
		return nil, fmt.Errorf("templated trace: %w", err)
	}
	for i, d := range m["exchanges"].([]any) {
		var ex Exchange
		if err := fromGeneric(d, &ex); err != nil {
			return nil, fmt.Errorf("templated exchange %d: %w", i, err)
		}
		ex.Index = i
		if ex.Key, err = keyOf(ex.API, d.(map[string]any)["request"]); err != nil {
			return nil, fmt.Errorf("exchange %d key: %w", i, err)
		}
		f.Exchanges = append(f.Exchanges, ex)
	}
	if f.Contracts, err = Draft(f.Trace, in.ExpectFail); err != nil {
		return nil, err
	}
	if f.Contracts == nil {
		f.Contracts = []Contract{}
	}
	if f.Verification, err = Verify(f.Trace, f.Contracts, in.ExpectFail, check); err != nil {
		return nil, err
	}
	return f, nil
}

func toGeneric(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeJSON(raw)
}

func fromGeneric(v, into any) error {
	raw, err := canonical(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

// namePattern bounds fixture names: they become file names.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateName rejects names that are not safe file name stems.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("fixture name %q: use 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit", name)
	}
	return nil
}

// FixtureFile is the file name for a fixture.
func FixtureFile(name string) string { return name + ".attest.json" }

// WriteFile writes data to path atomically, creating parent directories.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".attest-capture-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
