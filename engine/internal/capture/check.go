package capture

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/attest-ai/attest/engine/internal/redact"
	"github.com/attest-ai/attest/engine/internal/trace"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// RedactionHit locates a secret in a fixture: Path is the JSON path of the
// string (or object key) holding it. The secret itself is never reported.
type RedactionHit struct {
	Kind string
	Path string
}

// CheckRedaction scans every string and object key of a JSON document for
// secrets the redactor knows: literal values and key-shaped strings.
func CheckRedaction(data []byte, r *redact.Redactor) ([]RedactionHit, error) {
	doc, err := decodeJSON(data)
	if err != nil {
		return nil, fmt.Errorf("not a JSON document: %w", err)
	}
	var hits []RedactionHit
	var walk func(v any, path string)
	scan := func(s, path string) {
		for _, h := range r.Scan(s) {
			hits = append(hits, RedactionHit{Kind: h.Kind, Path: path})
		}
	}
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for i, k := range keys {
				// A key holding a secret must not reach the printed path.
				segment := k
				if len(r.Scan(k)) > 0 {
					segment = fmt.Sprintf("<redacted key #%d>", i)
				}
				child := path + "." + segment
				scan(k, child+" (key)")
				walk(x[k], child)
			}
		case []any:
			for i, e := range x {
				walk(e, path+"["+strconv.Itoa(i)+"]")
			}
		case string:
			scan(x, path)
		}
	}
	walk(doc, "$")
	streamHits, err := checkStreams(doc, r)
	if err != nil {
		return nil, err
	}
	return append(hits, streamHits...), nil
}

// checkStreams scans the rebuilt delta text of every event-stream response,
// where a secret split across deltas is whole.
func checkStreams(doc any, r *redact.Redactor) ([]RedactionHit, error) {
	var hits []RedactionHit
	for i, e := range asSlice(asMap(doc)["exchanges"]) {
		ex := asMap(e)
		resp := asMap(ex["response"])
		ct, _ := resp["content_type"].(string)
		requested, _ := ex["stream"].(bool)
		body, ok := resp["body"].(string)
		if !ok || !isEventStream(ct, requested) {
			continue
		}
		texts, err := streamTexts(body)
		if err != nil {
			return nil, fmt.Errorf("exchanges[%d]: unparseable event stream: %w", i, err)
		}
		ids := make([]string, 0, len(texts))
		for id := range texts {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			for _, h := range r.Scan(texts[id]) {
				hits = append(hits, RedactionHit{Kind: h.Kind, Path: fmt.Sprintf("$.exchanges[%d].response.body (stream %q)", i, id)})
			}
		}
	}
	return hits, nil
}

// Fixture check issue codes.
const (
	IssueInvalidFixture = "invalid_fixture"
	IssueInvalidTrace   = "invalid_trace"
	IssueCaptureCheck   = "capture_trace_check"
	IssueUnverified     = "unverified_fixture"
)

// IsFixture reports whether a JSON document is a capture fixture rather than
// an OTLP export.
func IsFixture(data []byte) bool {
	var probe struct {
		FixtureVersion *json.RawMessage `json:"fixture_version"`
	}
	return json.Unmarshal(data, &probe) == nil && probe.FixtureVersion != nil
}

// CheckFixture is trace check for a fixture: the fixture decodes, its trace
// passes the engine's trace validation, and the capture-time trace check was
// clean. An unverified fixture is reported as a warning.
func CheckFixture(data []byte) *trace.CheckReport {
	r := &trace.CheckReport{
		SchemaVersion: types.TraceSchemaVersion2,
		Steps:         map[string]int{},
		Issues:        []trace.Issue{},
		Coverage:      []trace.CoverageGap{},
	}
	add := func(sev, code, msg string) {
		r.Issues = append(r.Issues, trace.Issue{Severity: sev, Code: code, Message: msg})
	}
	f, err := Unmarshal(data)
	if err != nil {
		add(trace.SeverityError, IssueInvalidFixture, err.Error())
		return r
	}
	t := f.Trace
	r.TraceID = t.TraceID
	for _, s := range t.Steps {
		r.Steps[s.Type]++
		if s.RetryOf != nil {
			r.Retries++
		}
	}
	r.Turns, r.Handoffs = len(t.Turns), len(t.Handoffs)
	if rpcErr := trace.Validate(t, len(data)); rpcErr != nil {
		add(trace.SeverityError, IssueInvalidTrace, rpcErr.Message)
	}
	if tc := f.Verification.TraceCheck; !tc.OK {
		add(trace.SeverityError, IssueCaptureCheck, fmt.Sprintf("the captured spans failed trace check with %d errors", tc.Errors))
	}
	if !f.Verification.Verified {
		add(trace.SeverityWarning, IssueUnverified, "fixture is not verified; its drafted contracts did not hold at capture")
	}
	r.OK = len(r.Errors()) == 0
	return r
}
