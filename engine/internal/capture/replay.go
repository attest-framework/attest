package capture

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/attest-framework/attest/engine/internal/redact"
)

// Replay modes. Strict fails a request that matches no recording; auto
// forwards it live when a provider key is set.
const (
	ReplayStrict = "strict"
	ReplayAuto   = "auto"
)

// Replay outcomes of one request.
const (
	OutcomeReplayed = "replayed"
	OutcomeLive     = "live"
	OutcomeMiss     = "miss"
)

// Fixture loading limits.
const (
	MaxFixtureBytes     = 256 << 20
	MaxFixtureExchanges = 10000
)

// fixtureLimit is MaxFixtureBytes; tests lower it.
var fixtureLimit int64 = MaxFixtureBytes

// ReplayOptions configures a replay session.
type ReplayOptions struct {
	Mode    string
	Fixture *Fixture
	Environ []string // the process environment: redaction secrets and live provider keys
	// Redactor holds further redaction secrets, such as the working directory's
	// .env that capture also redacted with; nil adds none.
	Redactor *redact.Redactor
	// NoLive refuses every live forward: a miss in auto mode is reported, never sent.
	NoLive    bool
	Upstreams Upstreams
}

// ReplayRecord is the outcome of one request the agent made.
type ReplayRecord struct {
	Seq      int    `json:"seq"`
	Provider string `json:"provider"`
	API      string `json:"api"`
	Outcome  string `json:"outcome"`
	// Exchange is the fixture exchange index a replayed request consumed.
	Exchange *int   `json:"exchange,omitempty"`
	Message  string `json:"message,omitempty"`
}

// ReplaySummary is what a replay session served, printed when it ends.
type ReplaySummary struct {
	Fixture  string `json:"fixture"`
	Mode     string `json:"mode"`
	Replayed int    `json:"replayed"`
	Live     int    `json:"live"`
	Misses   int    `json:"misses"`
	// Unused counts recordings the agent never requested.
	Unused int `json:"unused"`
	// LiveCostUSD prices the live calls whose model has a built-in price;
	// UnpricedLiveCalls counts the rest.
	LiveCostUSD       float64        `json:"live_cost_usd"`
	UnpricedLiveCalls int            `json:"unpriced_live_calls"`
	Records           []ReplayRecord `json:"records"`
}

type replayEntry struct {
	ex       Exchange
	identity requestIdentity
	consumed bool
}

// Replayer answers provider requests from a fixture's recorded exchanges.
type Replayer struct {
	mode     string
	name     string
	redactor *redact.Redactor
	liveKeys map[string]string

	idMu sync.Mutex
	ids  *identityBinder // live identity labels, numbered across the session

	mu      sync.Mutex
	entries []*replayEntry
	queues  map[string][]int
	records []ReplayRecord
}

// LoadFixture reads and validates a fixture file. A file above
// MaxFixtureBytes is refused before it is read into memory.
func LoadFixture(path string) (*Fixture, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, fixtureLimit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if int64(len(data)) > fixtureLimit {
		return nil, fmt.Errorf("fixture %s is larger than %d bytes", path, fixtureLimit)
	}
	fx, err := Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return fx, nil
}

// validateForReplay checks what replay needs beyond the file format.
func validateForReplay(f *Fixture) error {
	if err := ValidateName(f.Name); err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	if !f.Source.ReplayCapable {
		return fmt.Errorf("fixture %q is trace-only (source %s) and cannot be replayed; capture it with a command or record()", f.Name, f.Source.Kind)
	}
	if len(f.Exchanges) == 0 {
		return fmt.Errorf("fixture %q recorded no model exchanges", f.Name)
	}
	if len(f.Exchanges) > MaxFixtureExchanges {
		return fmt.Errorf("fixture %q has %d exchanges; replay supports at most %d", f.Name, len(f.Exchanges), MaxFixtureExchanges)
	}
	for i, ex := range f.Exchanges {
		switch {
		case ex.Provider != "openai" && ex.Provider != "anthropic":
			return fmt.Errorf("fixture: exchanges[%d].provider %q is not supported", i, ex.Provider)
		case ex.Method == "" || !strings.HasPrefix(ex.Path, "/"):
			return fmt.Errorf("fixture: exchanges[%d] has no method or path", i)
		case ex.Response.Status < 100 || ex.Response.Status > 599:
			return fmt.Errorf("fixture: exchanges[%d].response.status %d is not an HTTP status", i, ex.Response.Status)
		case ex.Response.Truncated:
			return fmt.Errorf("fixture: exchanges[%d] response was truncated at capture and cannot be replayed; re-record with `attest capture --refresh %s`", i, f.Name)
		case len(ex.Request) == 0 || len(ex.Response.Body) == 0:
			return fmt.Errorf("fixture: exchanges[%d] has an empty request or response body", i)
		}
	}
	return nil
}

// replayRedactor returns the redactor replay normalizes requests with: the one
// capture used, so a secret redacted when recording is redacted again.
func replayRedactor(o ReplayOptions) *redact.Redactor {
	r := redact.FromEnviron(o.Environ)
	if o.Redactor != nil {
		r = r.Merge(o.Redactor)
	}
	return r
}

// NewReplayer builds the matcher for a fixture.
func NewReplayer(o ReplayOptions) (*Replayer, error) {
	if o.Mode != ReplayStrict && o.Mode != ReplayAuto {
		return nil, fmt.Errorf("replay mode %q: want %s or %s", o.Mode, ReplayStrict, ReplayAuto)
	}
	if o.Fixture == nil {
		return nil, fmt.Errorf("replay: no fixture")
	}
	if err := validateForReplay(o.Fixture); err != nil {
		return nil, err
	}
	r := &Replayer{
		mode:     o.Mode,
		name:     o.Fixture.Name,
		redactor: replayRedactor(o),
		liveKeys: liveKeys(o.Environ, o.NoLive),
		queues:   map[string][]int{},
	}
	recorded := newRecordedBinder()
	for i, ex := range o.Fixture.Exchanges {
		doc, err := decodeJSON(ex.Request)
		if err != nil {
			return nil, fmt.Errorf("fixture: exchanges[%d].request: %w", i, err)
		}
		// Stored requests are already redacted; the redactor here only guards
		// against a fixture that carries a secret of the current environment.
		norm, raw, err := normalizeRequest(doc, r.redactor, recorded)
		if err != nil {
			return nil, fmt.Errorf("fixture: exchanges[%d].request: %w", i, err)
		}
		id, err := identityOf(ex.Provider, ex.API, ex.Method, normalizePath(ex.Path, r.redactor), norm, raw)
		if err != nil {
			return nil, fmt.Errorf("fixture: exchanges[%d]: %w", i, err)
		}
		r.entries = append(r.entries, &replayEntry{ex: ex, identity: id})
		k := id.String()
		r.queues[k] = append(r.queues[k], i)
	}
	r.ids = recorded.liveBinder()
	return r, nil
}

// liveKeys returns, per provider, the standard key the environment holds. A
// missed request goes live only when the client presents exactly that key:
// the proxy forwards the client's credentials and adds none, and a request
// carrying some other credential must not reach a provider on the strength of
// an unrelated key in the environment. NoLive removes every key.
func liveKeys(environ []string, noLive bool) map[string]string {
	set := map[string]string{}
	if noLive {
		return set
	}
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		switch name {
		case "OPENAI_API_KEY":
			set["openai"] = value
		case "ANTHROPIC_API_KEY":
			set["anthropic"] = value
		}
	}
	return set
}

// mayGoLive reports whether a missed request carrying credential may be
// forwarded.
func (r *Replayer) mayGoLive(provider, credential string) bool {
	key := r.liveKeys[provider]
	return key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(credential)) == 1
}

// requestCredential is the provider credential a client sent: a bearer token,
// or the x-api-key / api-key header.
func requestCredential(h http.Header) string {
	if v, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer "); ok {
		return v
	}
	if v := h.Get("X-Api-Key"); v != "" {
		return v
	}
	return h.Get("Api-Key")
}

// decision is how the proxy answers one request.
type decision struct {
	hit  *replayEntry
	live bool
	rec  ReplayRecord // the record of a live request
	miss string
}

// decide matches a request against the recordings and records the outcome.
func (r *Replayer) decide(provider, method, path, credential string, body []byte) decision {
	api := apiOf(provider, path)
	var doc any = string(body)
	if len(body) > 0 {
		if v, err := decodeJSON(body); err == nil {
			doc = v
		}
	}
	var id requestIdentity
	r.idMu.Lock()
	norm, raw, err := normalizeRequest(doc, r.redactor, r.ids)
	r.idMu.Unlock()
	if err == nil {
		var livePath string
		if livePath, err = redactPath(path); err == nil {
			id, err = identityOf(provider, api, method, normalizePath(livePath, r.redactor), norm, raw)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := ReplayRecord{Provider: provider, API: api}
	if err != nil {
		rec.Outcome, rec.Message = OutcomeMiss, r.missMessage(fmt.Sprintf("the request could not be normalized (%s)", err))
		r.add(rec)
		return decision{miss: rec.Message}
	}
	for _, idx := range r.queues[id.String()] {
		if e := r.entries[idx]; !e.consumed {
			e.consumed = true
			rec.Outcome, rec.Exchange = OutcomeReplayed, &idx
			r.add(rec)
			return decision{hit: e}
		}
	}
	rec.Message = r.diagnose(id)
	if r.mode == ReplayAuto && r.mayGoLive(provider, credential) {
		// Recorded by the caller once the proxy has accepted the request.
		rec.Outcome = OutcomeLive
		return decision{live: true, rec: rec}
	}
	rec.Outcome = OutcomeMiss
	r.add(rec)
	return decision{miss: rec.Message}
}

// add appends a record, numbering it. Callers hold r.mu unless they come
// through record.
func (r *Replayer) add(rec ReplayRecord) {
	rec.Seq = len(r.records)
	r.records = append(r.records, rec)
}

// record appends the record of a request a decision let through.
func (r *Replayer) record(rec ReplayRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.add(rec)
}

func (r *Replayer) missMessage(what string) string {
	return fmt.Sprintf("replay miss: %s; re-record with `attest capture --refresh %s`", what, r.name)
}

// diagnose compares a request that matched nothing with the next unconsumed
// recording and names the first component that differs. Callers hold r.mu.
func (r *Replayer) diagnose(id requestIdentity) string {
	if len(r.queues[id.String()]) > 0 {
		return r.missMessage("the agent made an extra identical call; every recording of this request was already replayed")
	}
	var target *replayEntry
	for _, e := range r.entries {
		if e.consumed {
			continue
		}
		if e.identity.provider == id.provider && e.identity.api == id.api {
			target = e
			break
		}
		if target == nil {
			target = e
		}
	}
	switch {
	case target == nil:
		return r.missMessage(fmt.Sprintf("the agent made more model calls than the %d recorded", len(r.entries)))
	case target.identity.provider != id.provider || target.identity.api != id.api:
		return r.missMessage(fmt.Sprintf("request to %s %s but the next recording is %s %s", id.provider, id.api, target.identity.provider, target.identity.api))
	}
	if d := firstDifference(target.identity.comps, id.comps); d != "" {
		return r.missMessage(d)
	}
	return r.missMessage("the request matches no recording")
}

// Summary reports what the session served. Live cost is filled in by the
// caller from the proxy's log.
func (r *Replayer) Summary() ReplaySummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := ReplaySummary{Fixture: r.name, Mode: r.mode, Records: append([]ReplayRecord{}, r.records...)}
	for _, rec := range r.records {
		switch rec.Outcome {
		case OutcomeReplayed:
			s.Replayed++
		case OutcomeLive:
			s.Live++
		default:
			s.Misses++
		}
	}
	for _, e := range r.entries {
		if !e.consumed {
			s.Unused++
		}
	}
	return s
}

// writeReplayed answers w with the recorded response. Event streams are
// written one event at a time and flushed, as a provider would send them.
func writeReplayed(w http.ResponseWriter, ex Exchange) {
	h := w.Header()
	if ex.Response.ContentType != "" {
		h.Set("Content-Type", ex.Response.ContentType)
	}
	h.Set("X-Attest-Replay", OutcomeReplayed)
	body := bytes.TrimSpace(ex.Response.Body)
	if len(body) > 0 && body[0] == '"' {
		var text string
		if err := json.Unmarshal(body, &text); err == nil {
			w.WriteHeader(ex.Response.Status)
			flusher, _ := w.(http.Flusher)
			for _, chunk := range splitEvents(text) {
				_, _ = io.WriteString(w, chunk)
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		compact.Reset()
		compact.Write(body)
	}
	w.WriteHeader(ex.Response.Status)
	_, _ = w.Write(compact.Bytes())
}

// splitEvents cuts an event stream after each blank line, keeping the
// separators, so the pieces concatenate back to the recording.
func splitEvents(text string) []string {
	var out []string
	for len(text) > 0 {
		i := strings.Index(text, "\n\n")
		if i < 0 {
			out = append(out, text)
			break
		}
		out = append(out, text[:i+2])
		text = text[i+2:]
	}
	return out
}

// writeMiss answers a request that has no recording, in the provider's error
// shape. 422 is not retried by the official SDKs, so a miss is reported once.
func writeMiss(w http.ResponseWriter, provider, message string) {
	errBody := map[string]any{"type": "attest_replay_miss", "message": message}
	doc := map[string]any{"error": errBody}
	if provider == "anthropic" {
		doc = map[string]any{"type": "error", "error": errBody}
	}
	b, _ := json.Marshal(doc)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Attest-Replay", OutcomeMiss)
	w.WriteHeader(http.StatusUnprocessableEntity)
	_, _ = w.Write(b)
}
