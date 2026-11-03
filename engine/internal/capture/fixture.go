package capture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"regexp"
	"strings"

	"github.com/attest-ai/attest/engine/internal/redact"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// FixtureVersion is the fixture-v1 schema version
// (proto/attest/v1/fixture-v1.schema.json).
const FixtureVersion = 1

// Source kinds.
const (
	SourceCommand   = "command"
	SourceInProcess = "in_process"
	SourceOTLPFile  = "otlp_file"
)

// Fixture is a captured run: its trace, its model exchanges, the contracts
// drafted from it and their verification.
type Fixture struct {
	FixtureVersion int             `json:"fixture_version"`
	Name           string          `json:"name"`
	Source         Source          `json:"source"`
	Trace          *types.Trace    `json:"trace"`
	Exchanges      []Exchange      `json:"exchanges"`
	Contracts      []Contract      `json:"contracts"`
	Verification   Verification    `json:"verification"`
	World          json.RawMessage `json:"world"`
}

// Source says how the fixture was captured. Only command and in-process
// captures record model exchanges, so only they can be replayed.
type Source struct {
	Kind          string   `json:"kind"`
	ReplayCapable bool     `json:"replay_capable"`
	Command       []string `json:"command,omitempty"`
	ExitCode      *int     `json:"exit_code,omitempty"`
}

// Exchange is one recorded model call.
type Exchange struct {
	Index    int    `json:"index"`
	Provider string `json:"provider"`
	// API is chat_completions, responses or messages; other for any other
	// endpoint (recorded, keyed on its whole body).
	API      string          `json:"api"`
	Method   string          `json:"method"`
	Path     string          `json:"path"`
	Stream   bool            `json:"stream"`
	Key      Key             `json:"key"`
	Request  json.RawMessage `json:"request"`
	Response Response        `json:"response"`
}

// Key is an exchange's replay identity: what must match for a recorded
// response to answer a new request.
type Key struct {
	Model          string          `json:"model"`
	Params         json.RawMessage `json:"params"`
	PromptHash     string          `json:"prompt_hash"`
	ToolSchemaHash *string         `json:"tool_schema_hash"`
}

// Response is a recorded upstream response. Body is the decoded JSON value
// for JSON responses and a string for event streams and other text.
type Response struct {
	Status      int             `json:"status"`
	ContentType string          `json:"content_type"`
	Body        json.RawMessage `json:"body"`
	Truncated   bool            `json:"truncated,omitempty"`
}

// Contract is a drafted deterministic contract, in the engine's assertion
// spec form (the form the SDK DSL emits).
type Contract struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Description string          `json:"description"`
	Type        string          `json:"type"`
	Spec        json.RawMessage `json:"spec"`
}

// Verification records the contracts' results on the captured trace.
type Verification struct {
	Verified         bool             `json:"verified"`
	ExpectFail       bool             `json:"expect_fail"`
	TraceCheck       TraceCheckResult `json:"trace_check"`
	Results          []ContractResult `json:"results"`
	ExpectedFailures []string         `json:"expected_failures"`
}

// TraceCheckResult summarizes `trace check` on the captured spans.
type TraceCheckResult struct {
	OK       bool `json:"ok"`
	Errors   int  `json:"errors"`
	Warnings int  `json:"warnings"`
}

// ContractResult is one contract's status on the captured trace.
type ContractResult struct {
	Contract    string `json:"contract"`
	Status      string `json:"status"`
	Explanation string `json:"explanation"`
}

// prompt fields per API: the request fields hashed into Key.PromptHash.
var promptFields = map[string][]string{
	"chat_completions": {"messages"},
	"responses":        {"input", "instructions"},
	"messages":         {"system", "messages"},
}

// apiOf names the provider API an exchange path belongs to.
func apiOf(provider, path string) string {
	p, _, _ := strings.Cut(path, "?")
	switch {
	case provider == "openai" && strings.HasSuffix(p, "/chat/completions"):
		return "chat_completions"
	case provider == "openai" && strings.HasSuffix(p, "/responses"):
		return "responses"
	case provider == "anthropic" && strings.HasSuffix(p, "/v1/messages"):
		return "messages"
	default:
		return "other"
	}
}

// exchangeDoc is the generic form of one exchange. An event-stream body has
// secrets that span deltas redacted here, before the per-string redaction of
// the whole fixture; a stream that does not parse fails the capture.
func exchangeDoc(ex rawExchange, r *redact.Redactor) (map[string]any, error) {
	req, err := bodyValue(ex.request, "application/json")
	if err != nil {
		return nil, fmt.Errorf("exchange %d request: %w", ex.seq, err)
	}
	resp, err := bodyValue(ex.response, ex.contentType)
	if err != nil {
		return nil, fmt.Errorf("exchange %d response: %w", ex.seq, err)
	}
	requested := false
	if m, ok := req.(map[string]any); ok {
		requested, _ = m["stream"].(bool)
	}
	stream := isEventStream(ex.contentType, requested)
	// A failed streaming request answers with an error (JSON, an HTML 502, a
	// text 429), not a stream: it is a stream body only when its media type
	// says so. The fixture-wide per-string redaction covers the error body.
	succeeded := ex.status >= 200 && ex.status <= 299
	if (isEventStream(ex.contentType, false) || (stream && succeeded)) && len(ex.response) > 0 {
		text, err := redactSSE(string(ex.response), r)
		if err != nil {
			return nil, fmt.Errorf("exchange %d (%s %s): unparseable event stream, no fixture written: %w", ex.seq, ex.method, ex.path, err)
		}
		resp = text
	}
	path, err := redactPath(ex.path)
	if err != nil {
		return nil, fmt.Errorf("exchange %d path: %w", ex.seq, err)
	}
	doc := map[string]any{
		"provider": ex.provider,
		"api":      apiOf(ex.provider, path),
		"method":   ex.method,
		"path":     path,
		"stream":   stream,
		"request":  req,
		"response": map[string]any{
			"status":       json.Number(fmt.Sprint(ex.status)),
			"content_type": ex.contentType,
			"body":         resp,
		},
	}
	if ex.truncated {
		doc["response"].(map[string]any)["truncated"] = true
	}
	return doc, nil
}

// secretQueryParams are query parameters that carry credentials.
var secretQueryParams = map[string]bool{
	"key": true, "api_key": true, "apikey": true, "token": true, "access_token": true, "sig": true, "signature": true,
}

// redactPath takes an exchange path as recorded, still percent-escaped,
// decodes it once so encoded secrets meet the redactor's patterns, and
// replaces the values of credential query parameters with the marker. The
// decoded form is stored; a literal % (escaped as %25) decodes to %.
func redactPath(raw string) (string, error) {
	p, q, hasQuery := strings.Cut(raw, "?")
	path, err := url.PathUnescape(p)
	if err != nil {
		return "", err
	}
	if !hasQuery {
		return path, nil
	}
	var parts []string
	for _, kv := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(kv, "=")
		name, err := url.QueryUnescape(k)
		if err != nil {
			return "", err
		}
		value, err := url.QueryUnescape(v)
		if err != nil {
			return "", err
		}
		if secretQueryParams[strings.ToLower(name)] {
			value = redact.Marker
		}
		parts = append(parts, name+"="+value)
	}
	return path + "?" + strings.Join(parts, "&"), nil
}

// envAssignment matches an argv entry of the form NAME=value, as given to
// env(1) or a shell.
var envAssignment = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=`)

// redactArgv replaces the values of secret-named flags (--flag=value, and the
// argument after a bare --flag) and of NAME=value entries whose NAME marks a
// secret by the environment-name rule. Key-shaped values anywhere are left to
// the fixture-wide redaction. One-letter flags (-p value) are recorded as
// they are: secrets belong in the environment.
func redactArgv(argv []string) []string {
	out := make([]string, len(argv))
	copy(out, argv)
	for i := 0; i < len(out); i++ {
		a := out[i]
		if m := envAssignment.FindStringSubmatch(a); m != nil {
			if redact.IsSecretEnvName(m[1]) {
				out[i] = m[0] + redact.Marker
			}
			continue
		}
		if i == 0 || !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		// A one-letter flag (-p) is a short flag: recorded as is.
		if len(name) <= 1 || !redact.IsSecretArgName(name) {
			continue
		}
		if hasValue {
			out[i] = a[:strings.Index(a, "=")+1] + redact.Marker
		} else if i+1 < len(out) {
			out[i+1] = redact.Marker
			i++
		}
	}
	return out
}

// bodyValue decodes a JSON body, or returns the text as a string for any
// other content type or for a JSON body that does not parse.
func bodyValue(body []byte, contentType string) (any, error) {
	if len(body) == 0 {
		return "", nil
	}
	ct, _, _ := mime.ParseMediaType(contentType)
	if ct == "application/json" || strings.HasSuffix(ct, "+json") {
		v, err := decodeJSON(body)
		if err == nil {
			return v, nil
		}
	}
	return string(body), nil
}

func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

// keyOf computes an exchange's replay key from its templated request.
func keyOf(api string, request any) (Key, error) {
	body, _ := request.(map[string]any)
	k := Key{Params: json.RawMessage("{}")}
	if body == nil {
		h, err := hashOf(request)
		if err != nil {
			return k, err
		}
		k.PromptHash = h
		return k, nil
	}
	k.Model, _ = body["model"].(string)
	fields, known := promptFields[api]
	prompt := map[string]any{}
	params := map[string]any{}
	for name, v := range body {
		switch {
		case name == "model" || name == "tools":
		case !known || contains(fields, name):
			prompt[name] = v
		default:
			params[name] = v
		}
	}
	var err error
	if k.PromptHash, err = hashOf(prompt); err != nil {
		return k, err
	}
	if k.Params, err = canonical(params); err != nil {
		return k, err
	}
	if tools, ok := body["tools"]; ok {
		h, err := hashOf(tools)
		if err != nil {
			return k, err
		}
		k.ToolSchemaHash = &h
	}
	return k, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// canonical marshals v with sorted object keys and no HTML escaping.
func canonical(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func hashOf(v any) (string, error) {
	b, err := canonical(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// redactTree replaces secrets in every string and object key of v. Two keys
// that redact to the same text are an error: keeping either would drop the
// other's value depending on map order.
func redactTree(v any, r *redact.Redactor) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			rk := r.String(k)
			if _, dup := out[rk]; dup {
				return nil, fmt.Errorf("two object keys redact to %q; rename the keys that hold secrets", rk)
			}
			re, err := redactTree(e, r)
			if err != nil {
				return nil, err
			}
			out[rk] = re
		}
		return out, nil
	case []any:
		for i, e := range x {
			re, err := redactTree(e, r)
			if err != nil {
				return nil, err
			}
			x[i] = re
		}
		return x, nil
	case string:
		return r.String(x), nil
	default:
		return v, nil
	}
}

// Marshal encodes a fixture with sorted keys, two-space indent and no HTML
// escaping, so the same capture always produces the same bytes.
func Marshal(f *Fixture) ([]byte, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	doc, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Unmarshal decodes a fixture file and checks the invariants the schema
// cannot express.
func Unmarshal(data []byte) (*Fixture, error) {
	var f Fixture
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("fixture: %w", err)
	}
	if f.FixtureVersion != FixtureVersion {
		return nil, fmt.Errorf("fixture: fixture_version %d is not supported (want %d)", f.FixtureVersion, FixtureVersion)
	}
	if f.Trace == nil {
		return nil, fmt.Errorf("fixture: trace is required")
	}
	for i, ex := range f.Exchanges {
		if ex.Index != i {
			return nil, fmt.Errorf("fixture: exchanges[%d] has index %d", i, ex.Index)
		}
	}
	ids := map[string]bool{}
	for _, c := range f.Contracts {
		if ids[c.ID] {
			return nil, fmt.Errorf("fixture: duplicate contract id %q", c.ID)
		}
		ids[c.ID] = true
	}
	for _, r := range f.Verification.Results {
		if !ids[r.Contract] {
			return nil, fmt.Errorf("fixture: verification result names unknown contract %q", r.Contract)
		}
	}
	for _, id := range f.Verification.ExpectedFailures {
		if !ids[id] {
			return nil, fmt.Errorf("fixture: expected failure names unknown contract %q", id)
		}
	}
	return &f, nil
}
