package world

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Limits that bound what a client can make the world hold.
const (
	// MaxBodyBytes bounds one request body.
	MaxBodyBytes = 1 << 20
	// MaxRecords bounds the records across all resources, seed included.
	MaxRecords = 10000
	// MaxStateBytes bounds the serialized size of all records.
	MaxStateBytes = 1 << 20
	// MaxFaultsFired bounds the faults a snapshot lists; the count is exact.
	MaxFaultsFired = 1000
	// MaxHold caps how long timeout_after_commit holds a response.
	MaxHold = 30 * time.Second
	// maxKeyLen bounds an idempotency key.
	maxKeyLen = 255
	// StatePath is the snapshot route, relative to the world's base URL.
	// Resource paths start with a letter, so it cannot collide.
	StatePath = "/_attest/state"
)

// Fired records one fault that fired.
type Fired struct {
	ID       string    `json:"id,omitempty"`
	Resource string    `json:"resource"`
	Route    Route     `json:"route"`
	Fault    FaultKind `json:"fault"`
	Request  int       `json:"request"`
}

// Snapshot is the world-v1 snapshot: the state and the faults fired so far.
type Snapshot struct {
	WorldVersion     int                         `json:"world_version"`
	DefinitionSHA256 string                      `json:"definition_sha256"`
	State            map[string][]map[string]any `json:"state"`
	FaultsFired      []Fired                     `json:"faults_fired"`
	// FaultsFiredCount is every fault that fired; FaultsFired keeps the first MaxFaultsFired.
	FaultsFiredCount int `json:"faults_fired_count"`
}

type store struct {
	records []map[string]any
	byID    map[string]int
	created int
}

type replayEntry struct {
	bodyHash string
	id       string
}

type routeKey struct {
	resource string
	route    Route
}

// World is a running fake. It is safe for concurrent use; requests run one
// at a time through the state, so a fixed request sequence and seed give the
// same outcomes.
type World struct {
	def    *Definition
	byPath map[string]string

	mu       sync.Mutex
	stores   map[string]*store
	replays  map[string]map[string]replayEntry
	ordinals map[routeKey]int
	rngs     map[int]*rand.Rand
	fired    []Fired
	// firedCount is every fault that fired; fired keeps the first MaxFaultsFired.
	firedCount int
	records    int
	bytes      int

	// holdCap is MaxHold; tests shorten it.
	holdCap     time.Duration
	released    chan struct{}
	releaseOnce sync.Once
}

// New builds a world from a validated definition with its seed records.
func New(def *Definition) (*World, error) {
	w := &World{
		def:      def,
		byPath:   map[string]string{},
		stores:   map[string]*store{},
		replays:  map[string]map[string]replayEntry{},
		ordinals: map[routeKey]int{},
		rngs:     map[int]*rand.Rand{},
		holdCap:  MaxHold,
		released: make(chan struct{}),
	}
	for name, r := range def.Resources {
		w.byPath[r.Path] = name
		st := &store{byID: map[string]int{}}
		w.stores[name] = st
		w.replays[name] = map[string]replayEntry{}
		for _, rec := range r.Seed {
			n, err := sizeOf(rec)
			if err != nil {
				return nil, err
			}
			if w.records+1 > MaxRecords || w.bytes+n > MaxStateBytes {
				return nil, errors.New("world definition: seed records exceed the state limits")
			}
			w.records++
			w.bytes += n
			st.byID[rec[r.IDField].(string)] = len(st.records)
			st.records = append(st.records, rec)
		}
	}
	for i, f := range def.Faults {
		if f.OnRequests == nil {
			w.rngs[i] = rand.New(rand.NewPCG(def.Seed, uint64(i)))
		}
	}
	return w, nil
}

// BaseURLEnv is the environment variable that carries the world's base URL.
func (w *World) BaseURLEnv() string { return w.def.BaseURLEnv }

// Release unblocks held responses so the server can shut down.
func (w *World) Release() { w.releaseOnce.Do(func() { close(w.released) }) }

// Snapshot returns a copy of the current state.
func (w *World) Snapshot() *Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := &Snapshot{
		WorldVersion:     Version,
		DefinitionSHA256: w.def.Hash,
		State:            make(map[string][]map[string]any, len(w.stores)),
		FaultsFired:      append([]Fired{}, w.fired...),
		FaultsFiredCount: w.firedCount,
	}
	for name, st := range w.stores {
		recs := make([]map[string]any, len(st.records))
		for i, r := range st.records {
			recs[i] = cloneRecord(r)
		}
		s.State[name] = recs
	}
	return s
}

func cloneRecord(r map[string]any) map[string]any {
	b, _ := json.Marshal(r)
	out := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&out)
	return out
}

func sizeOf(v any) (int, error) {
	b, err := json.Marshal(v)
	return len(b), err
}

// Handler serves the world's routes under prefix ("" or "/token/world").
func (w *World) Handler(prefix string) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rel := strings.TrimPrefix(req.URL.Path, prefix)
		if rel == StatePath {
			if req.Method != http.MethodGet {
				methodNotAllowed(rw, http.MethodGet)
				return
			}
			writeJSON(rw, http.StatusOK, w.Snapshot())
			return
		}
		w.serve(rw, req, rel)
	})
}

func (w *World) serve(rw http.ResponseWriter, req *http.Request, rel string) {
	segs := strings.Split(strings.TrimPrefix(rel, "/"), "/")
	name, ok := w.byPath["/"+segs[0]]
	if !ok || len(segs) > 2 || segs[len(segs)-1] == "" {
		writeError(rw, http.StatusNotFound, "not_found")
		return
	}
	res := w.def.Resources[name]
	var route Route
	allow := ""
	switch {
	case len(segs) == 1 && req.Method == http.MethodPost:
		route = RouteCreate
	case len(segs) == 1 && req.Method == http.MethodGet:
		route = RouteList
	case len(segs) == 2 && req.Method == http.MethodGet:
		route = RouteGet
	case len(segs) == 1:
		allow = "GET, POST"
	default:
		allow = http.MethodGet
	}
	if allow != "" {
		methodNotAllowed(rw, allow)
		return
	}
	if !res.has(route) {
		writeError(rw, http.StatusNotFound, "not_found")
		return
	}
	var body []byte
	if route == RouteCreate {
		var err error
		body, err = io.ReadAll(http.MaxBytesReader(rw, req.Body, MaxBodyBytes))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(rw, http.StatusRequestEntityTooLarge, "body_too_large")
			} else {
				writeError(rw, http.StatusBadRequest, "unreadable_body")
			}
			return
		}
	}

	w.mu.Lock()
	out := w.handle(name, res, route, segs, req, body)
	w.mu.Unlock()

	if out.hold {
		select {
		case <-req.Context().Done():
		case <-w.released:
		case <-time.After(w.holdCap):
		}
		// The commit stands; the client never sees the response.
		panic(http.ErrAbortHandler)
	}
	for k, v := range out.header {
		rw.Header().Set(k, v)
	}
	writeJSON(rw, out.status, out.body)
}

type outcome struct {
	status int
	body   any
	header map[string]string
	hold   bool
}

func errOutcome(status int, code string) outcome {
	return outcome{status: status, body: map[string]string{"error": code}}
}

// handle runs with w.mu held.
func (w *World) handle(name string, res *Resource, route Route, segs []string, req *http.Request, body []byte) outcome {
	key := routeKey{name, route}
	w.ordinals[key]++
	ordinal := w.ordinals[key]

	if f, fired := w.faultFor(name, route, ordinal); fired {
		rec := Fired{ID: f.ID, Resource: name, Route: route, Fault: f.Kind, Request: ordinal}
		if st := f.Kind.Status(); st != 0 {
			w.recordFired(rec)
			return errOutcome(st, "injected_fault")
		}
		// timeout_after_commit: commit below, then hold.
		o := w.create(name, res, req, body)
		if o.status == http.StatusCreated {
			o.hold = true
			w.recordFired(rec)
		}
		return o
	}
	switch route {
	case RouteCreate:
		return w.create(name, res, req, body)
	case RouteList:
		return w.list(name, res, req)
	default:
		st := w.stores[name]
		i, ok := st.byID[segs[1]]
		if !ok {
			return errOutcome(http.StatusNotFound, name+"_not_found")
		}
		return outcome{status: http.StatusOK, body: st.records[i]}
	}
}

// recordFired counts a fired fault and keeps the first MaxFaultsFired of them,
// charged against the state size so a long run cannot grow the snapshot
// without bound.
func (w *World) recordFired(f Fired) {
	w.firedCount++
	n, err := sizeOf(f)
	if len(w.fired) >= MaxFaultsFired || err != nil || w.bytes+n > MaxStateBytes {
		return
	}
	w.bytes += n
	w.fired = append(w.fired, f)
}

// faultFor returns the first fault that fires for this request. Every
// probabilistic fault on the route draws once per request, fired or not, so a
// stream depends only on the request sequence.
func (w *World) faultFor(name string, route Route, ordinal int) (Fault, bool) {
	var hit Fault
	found := false
	for i, f := range w.def.Faults {
		if f.Resource != name || f.Route != route {
			continue
		}
		fire := false
		if f.OnRequests != nil {
			for _, n := range f.OnRequests {
				fire = fire || n == ordinal
			}
		} else {
			fire = w.rngs[i].Float64() < f.Probability
		}
		if fire && !found {
			hit, found = f, true
		}
	}
	return hit, found
}

func (w *World) list(name string, res *Resource, req *http.Request) outcome {
	q := req.URL.Query()
	for k := range q {
		allowed := false
		for _, f := range res.Filters {
			allowed = allowed || f == k
		}
		if !allowed {
			return errOutcome(http.StatusBadRequest, "unknown_filter_"+sanitizeToken(k))
		}
	}
	recs := []map[string]any{}
	for _, r := range w.stores[name].records {
		match := true
		for k, vs := range q {
			match = match && valueString(r[k]) == vs[0]
		}
		if match {
			recs = append(recs, r)
		}
	}
	return outcome{status: http.StatusOK, body: map[string]any{name: recs}}
}

func sanitizeToken(s string) string {
	if len(s) > 32 {
		s = s[:32]
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

// valueString renders a scalar the way a query parameter would carry it.
func valueString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return "null"
	case json.Number:
		return t.String()
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func (w *World) create(name string, res *Resource, req *http.Request, body []byte) outcome {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var rec map[string]any
	if err := dec.Decode(&rec); err != nil || rec == nil {
		return errOutcome(http.StatusBadRequest, "body_must_be_a_json_object")
	}
	if _, more := dec.Token(); !errors.Is(more, io.EOF) {
		return errOutcome(http.StatusBadRequest, "body_must_be_a_json_object")
	}
	if err := CheckNumbers(rec); err != nil {
		return errOutcome(http.StatusBadRequest, "number_out_of_range")
	}
	if _, set := rec[res.IDField]; set {
		return errOutcome(http.StatusBadRequest, res.IDField+"_is_assigned_by_the_server")
	}
	st := w.stores[name]
	var key, hash string
	if res.Idempotency != nil {
		if _, set := rec[res.Idempotency.Field]; set {
			return errOutcome(http.StatusBadRequest, res.Idempotency.Field+"_is_set_from_the_"+res.Idempotency.Header+"_header")
		}
		key = req.Header.Get(res.Idempotency.Header)
		if len(key) > maxKeyLen {
			return errOutcome(http.StatusBadRequest, "idempotency_key_too_long")
		}
	}
	if key != "" {
		canon, _ := json.Marshal(rec)
		sum := sha256.Sum256(canon)
		hash = hex.EncodeToString(sum[:])
		if prev, seen := w.replays[name][key]; seen {
			if prev.bodyHash != hash {
				return errOutcome(http.StatusUnprocessableEntity, "idempotency_key_reused_with_different_body")
			}
			return outcome{status: http.StatusOK, body: st.records[st.byID[prev.id]], header: map[string]string{"Idempotent-Replayed": "true"}}
		}
		rec[res.Idempotency.Field] = key
	}
	id := w.nextID(res, st)
	rec[res.IDField] = id
	n, err := sizeOf(rec)
	if err != nil || w.records+1 > MaxRecords || w.bytes+n > MaxStateBytes {
		return errOutcome(http.StatusInsufficientStorage, "world_state_full")
	}
	w.records++
	w.bytes += n
	st.byID[id] = len(st.records)
	st.records = append(st.records, rec)
	if key != "" {
		w.replays[name][key] = replayEntry{bodyHash: hash, id: id}
	}
	return outcome{status: http.StatusCreated, body: rec}
}

func (w *World) nextID(res *Resource, st *store) string {
	for {
		st.created++
		id := fmt.Sprintf(res.IDFormat, st.created)
		if _, taken := st.byID[id]; !taken {
			return id
		}
	}
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(rw, "world: encode response", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_, _ = rw.Write(append(b, '\n'))
}

func writeError(rw http.ResponseWriter, status int, code string) {
	writeJSON(rw, status, map[string]string{"error": code})
}

func methodNotAllowed(rw http.ResponseWriter, allow string) {
	rw.Header().Set("Allow", allow)
	writeError(rw, http.StatusMethodNotAllowed, "method_not_allowed")
}

// Counts returns the number of records per resource, sorted by name, for
// summaries.
func (s *Snapshot) Counts() []string {
	names := make([]string, 0, len(s.State))
	for n := range s.State {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%d %s", len(s.State[n]), n)
	}
	return out
}
