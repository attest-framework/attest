package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/attest-ai/attest/engine/internal/redact"
	"github.com/attest-ai/attest/engine/internal/trace"
)

// Session is one capture: a loopback listener hosting the recording proxy
// and the OTLP/HTTP receiver. The agent under test reaches both through the
// environment variables Env returns.
type Session struct {
	proxy    *Proxy
	receiver *Receiver
	server   *http.Server
	listener net.Listener
	served   chan error
}

// Start opens the capture listener on an ephemeral 127.0.0.1 port.
// Upstreams are where the proxy forwards model calls.
func Start(up Upstreams) (*Session, error) {
	proxy, err := NewProxy(up)
	if err != nil {
		return nil, err
	}
	s := &Session{proxy: proxy, receiver: &Receiver{}, served: make(chan error, 1)}
	mux := http.NewServeMux()
	proxy.Register(mux)
	s.receiver.Register(mux)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("capture listener: %w", err)
	}
	s.listener = ln
	s.server = &http.Server{
		Handler:           loopbackOnly(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { s.served <- s.server.Serve(ln) }()
	return s, nil
}

// BaseURL is the listener's root URL.
func (s *Session) BaseURL() string { return "http://" + s.listener.Addr().String() }

// Env returns the variables that point an agent at the capture: provider base
// URLs for the proxy and the OTLP traces endpoint for the receiver.
func (s *Session) Env() map[string]string {
	base := s.BaseURL()
	return map[string]string{
		"OPENAI_BASE_URL":                    base + OpenAIPrefix,
		"ANTHROPIC_BASE_URL":                 base + AnthropicPrefix,
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": base + TracesPath,
	}
}

// Close stops accepting connections and waits for in-flight requests.
func (s *Session) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := s.server.Shutdown(ctx)
	if serr := <-s.served; serr != nil && !errors.Is(serr, http.ErrServerClosed) {
		return serr
	}
	return err
}

// Recorded returns what the session captured: spans, completed exchanges
// and the count of exchanges the client never finished reading.
func (s *Session) Recorded() ([]trace.Span, []rawExchange, int) {
	ex, incomplete := s.proxy.Exchanges()
	return s.receiver.Spans(), ex, incomplete
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
		d, err := exchangeDoc(ex)
		if err != nil {
			return nil, err
		}
		exDocs = append(exDocs, d)
	}
	command := make([]any, len(in.Source.Command))
	for i, a := range in.Source.Command {
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
