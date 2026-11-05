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

	"github.com/attest-framework/attest/engine/internal/redact"
	"github.com/attest-framework/attest/engine/internal/trace"
	"github.com/attest-framework/attest/engine/internal/world"
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
	// world is the simulated external system the session also hosts, nil
	// when none was requested.
	world *world.World
}

// Option configures a session.
type Option func(*sessionConfig)

type sessionConfig struct{ world *world.World }

// WithWorld hosts w on the session's listener. Env then carries w's base URL
// under the variable the definition names.
func WithWorld(w *world.World) Option { return func(c *sessionConfig) { c.world = w } }

// worldPrefix is the path segment, after the token, that serves the world.
const worldPrefix = "/world"

// Start opens the capture listener on an ephemeral 127.0.0.1 port.
// Upstreams are where the proxy forwards model calls.
func Start(up Upstreams, opts ...Option) (*Session, error) {
	proxy, err := NewProxy(up)
	if err != nil {
		return nil, err
	}
	return startWith(proxy, opts)
}

// StartWorld opens a listener that hosts only the world: no model proxy, no
// span receiver.
func StartWorld(w *world.World) (*Session, error) {
	return startWith(nil, []Option{WithWorld(w)})
}

// StartReplay starts a session whose proxy answers from a fixture. The
// receiver still runs, so an agent that exports spans does not fail; the
// spans are discarded with the session.
func StartReplay(o ReplayOptions, opts ...Option) (*Session, error) {
	rp, err := NewReplayer(o)
	if err != nil {
		return nil, err
	}
	proxy, err := NewProxy(o.Upstreams)
	if err != nil {
		return nil, err
	}
	proxy.replay = rp
	return startWith(proxy, opts)
}

// ReplaySummary reports what a replay session served. It is valid after
// Close, once every in-flight request has finished.
func (s *Session) ReplaySummary() ReplaySummary {
	if s.proxy == nil {
		return ReplaySummary{}
	}
	sum := s.proxy.replay.Summary()
	sum.LiveCostUSD, sum.UnpricedLiveCalls = s.proxy.liveCost()
	return sum
}

func startWith(proxy *Proxy, opts []Option) (*Session, error) {
	var cfg sessionConfig
	for _, o := range opts {
		o(&cfg)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("capture token: %w", err)
	}
	s := &Session{proxy: proxy, world: cfg.world, served: make(chan error, 1), token: hex.EncodeToString(raw)}
	mux := http.NewServeMux()
	if proxy != nil {
		s.receiver = &Receiver{budget: proxy.budget}
		proxy.Register(mux, "/"+s.token)
		s.receiver.Register(mux, "/"+s.token)
		if s.world != nil {
			if _, taken := s.modelEnv("")[s.world.BaseURLEnv()]; taken {
				return nil, fmt.Errorf("world base_url_env %s is a variable the capture session sets itself", s.world.BaseURLEnv())
			}
		}
	}
	if s.world != nil {
		prefix := "/" + s.token + worldPrefix
		mux.Handle(prefix+"/", s.world.Handler(prefix))
	}
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
	env := map[string]string{}
	if s.proxy != nil {
		env = s.modelEnv(base)
	}
	if s.world != nil {
		env[s.world.BaseURLEnv()] = base + worldPrefix
	}
	return env
}

func (s *Session) modelEnv(base string) map[string]string {
	return map[string]string{
		"OPENAI_BASE_URL":                    base + OpenAIPrefix,
		"ANTHROPIC_BASE_URL":                 base + AnthropicPrefix,
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": base + TracesPath,
	}
}

// WorldSnapshot is the hosted world's current state, nil without a world.
func (s *Session) WorldSnapshot() *world.Snapshot {
	if s.world == nil {
		return nil
	}
	return s.world.Snapshot()
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
	if s.world != nil {
		s.world.Release()
	}
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
	if s.proxy == nil {
		return nil, nil, 0, errors.New("session hosts only a world; nothing was recorded")
	}
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
	// World is the hosted world's final state; nil stores a null world
	// section.
	World *world.Snapshot
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
	tpl := newTemplater()
	doc = tpl.template(doc)
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
	if in.World != nil {
		if f.World, err = worldSection(in.World, in.Redactor, tpl); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// worldSection redacts the snapshot like the rest of the fixture and templates
// email addresses with the trace's templater, so one address is one
// {{email_N}} across the fixture. Ids and timestamps stay as the world held
// them: the state is evidence that effect contracts read literally.
func worldSection(snap *world.Snapshot, r *redact.Redactor, tpl *templater) (json.RawMessage, error) {
	doc, err := toGeneric(snap)
	if err != nil {
		return nil, err
	}
	doc, err = redactTree(doc, r)
	if err != nil {
		return nil, err
	}
	if doc, err = tpl.emails(doc); err != nil {
		return nil, err
	}
	raw, err := canonical(doc)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
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
