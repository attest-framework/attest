package trace

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// OTLP/JSON input limits. Input is untrusted (exported by any producer), so
// every dimension is bounded while decoding, before anything is retained.
const (
	MaxOTLPBytes = 32 << 20 // 32 MiB
	MaxOTLPSpans = 20000
	// MaxOTLPValues bounds attributes plus nested array and kvlist values
	// across the whole document.
	MaxOTLPValues    = 1_000_000
	MaxAnyValueDepth = 32
	MaxAttrsPerSpan  = 1024
	// maxSkipDepth bounds the nesting of fields the parser ignores.
	maxSkipDepth      = 64
	otlpStatusError   = 2
	traceIDHexLength  = 32
	spanIDHexLength   = 16
	statusErrorString = "STATUS_CODE_ERROR"

	// EventToolOutput is the span event Claude Code records a tool's output
	// on. It is the only span event the parser retains.
	EventToolOutput = "tool.output"
	// maxEventsPerSpan bounds the events inspected per span; maxKeptEvents
	// bounds the retained ones (the first maxKeptEvents tool.output events).
	// Every other event is decoded as JSON and dropped.
	maxEventsPerSpan = 256
	maxKeptEvents    = 4
)

// limitError is an input-limit violation. Limit errors always reject the
// export; other errors in a tool.output event only drop that event.
type limitError struct{ msg string }

func (e *limitError) Error() string { return e.msg }

// Event is a retained OTLP span event (only EventToolOutput events are kept).
type Event struct {
	Name       string
	Attributes map[string]any
}

// Span is one OTLP span reduced to the fields trace conversion and trace
// check read. IDs are lowercase hex. Attribute values are string, bool,
// int64, float64, []any or map[string]any.
type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	StartNs      uint64
	EndNs        uint64
	Attributes   map[string]any
	Events       []Event
	// DroppedEvents counts tool.output events dropped as malformed.
	DroppedEvents int
	StatusError   bool
	StatusMessage string
	// Order is the span's position in the input document.
	Order int
}

// ParseOTLP decodes an OTLP/JSON trace export (ExportTraceServiceRequest:
// resourceSpans → scopeSpans → spans). It streams tokens and enforces the
// span, attribute, value and nesting limits as it goes, so oversized input
// is rejected before it is materialized. Malformed input returns an error
// naming the offending location; it never panics.
func ParseOTLP(data []byte) ([]Span, error) {
	if len(data) > MaxOTLPBytes {
		return nil, fmt.Errorf("OTLP input is %d bytes; the limit is %d", len(data), MaxOTLPBytes)
	}
	p := &otlpParser{dec: json.NewDecoder(bytes.NewReader(data))}
	p.dec.UseNumber()
	if err := p.document(); err != nil {
		return nil, err
	}
	if _, err := p.dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("OTLP input has trailing data after the JSON document")
	}
	if len(p.spans) == 0 {
		return nil, errors.New("OTLP input contains no spans")
	}
	return p.spans, nil
}

type otlpParser struct {
	dec    *json.Decoder
	values int
	spans  []Span
}

func (p *otlpParser) token() (json.Token, error) {
	t, err := p.dec.Token()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("OTLP input is not valid OTLP/JSON: %w", io.ErrUnexpectedEOF)
	}
	if err != nil {
		return nil, fmt.Errorf("OTLP input is not valid OTLP/JSON: %w", err)
	}
	return t, nil
}

// object reads an object, calling field for each key with the decoder at the
// key's value. A JSON null counts as an absent object and returns false.
func (p *otlpParser) object(what string, field func(key string) error) (bool, error) {
	t, err := p.token()
	if err != nil {
		return false, err
	}
	if t == nil {
		return false, nil
	}
	if t != json.Delim('{') {
		return false, fmt.Errorf("%s must be an object", what)
	}
	for p.dec.More() {
		kt, err := p.token()
		if err != nil {
			return false, err
		}
		key, ok := kt.(string)
		if !ok {
			return false, fmt.Errorf("%s has a non-string key", what)
		}
		if err := field(key); err != nil {
			return false, err
		}
	}
	_, err = p.token()
	return true, err
}

// array reads an array, calling item for each element with the decoder at the
// element. A JSON null counts as an absent array and returns false.
func (p *otlpParser) array(what string, item func(i int) error) (bool, error) {
	t, err := p.token()
	if err != nil {
		return false, err
	}
	if t == nil {
		return false, nil
	}
	if t != json.Delim('[') {
		return false, fmt.Errorf("%s must be an array", what)
	}
	for i := 0; p.dec.More(); i++ {
		if err := item(i); err != nil {
			return false, err
		}
	}
	_, err = p.token()
	return true, err
}

// skip discards one value of a field the parser does not read.
func (p *otlpParser) skip() error {
	depth := 0
	for {
		t, err := p.token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'), json.Delim('['):
			depth++
			if depth > maxSkipDepth {
				return fmt.Errorf("OTLP input nests deeper than %d levels", maxSkipDepth)
			}
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}

// str reads a string or null ("").
func (p *otlpParser) str(field string) (string, error) {
	t, err := p.token()
	if err != nil {
		return "", err
	}
	switch v := t.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	}
	return "", fmt.Errorf("%s must be a string", field)
}

func (p *otlpParser) document() error {
	found := false
	ok, err := p.object("OTLP document", func(key string) error {
		if key != "resourceSpans" {
			return p.skip()
		}
		var err error
		found, err = p.array("resourceSpans", func(ri int) error {
			_, err := p.object(fmt.Sprintf("resourceSpans[%d]", ri), func(key string) error {
				if key != "scopeSpans" {
					return p.skip()
				}
				_, err := p.array("scopeSpans", func(si int) error {
					return p.scopeSpans(ri, si)
				})
				return err
			})
			return err
		})
		return err
	})
	if err != nil {
		return err
	}
	if !ok || !found {
		return errors.New("OTLP input has no resourceSpans array")
	}
	return nil
}

func (p *otlpParser) scopeSpans(ri, si int) error {
	_, err := p.object(fmt.Sprintf("resourceSpans[%d].scopeSpans[%d]", ri, si), func(key string) error {
		if key != "spans" {
			return p.skip()
		}
		_, err := p.array("spans", func(pi int) error {
			loc := fmt.Sprintf("resourceSpans[%d].scopeSpans[%d].spans[%d]", ri, si, pi)
			if len(p.spans) >= MaxOTLPSpans {
				return fmt.Errorf("OTLP input has more than %d spans", MaxOTLPSpans)
			}
			span, err := p.span()
			if err != nil {
				return fmt.Errorf("%s: %w", loc, err)
			}
			span.Order = len(p.spans)
			p.spans = append(p.spans, span)
			return nil
		})
		return err
	})
	return err
}

// rawSpan holds a span's fields as read, before validation.
type rawSpan struct {
	traceID, spanID, parentID, name string
	start, end                      *uint64
	attrs                           map[string]any
	events                          []Event
	droppedEvents                   int
	nEvents                         int
	statusError                     bool
	statusMessage                   string
	nAttrs                          int
}

func (p *otlpParser) span() (Span, error) {
	r := rawSpan{attrs: map[string]any{}}
	_, err := p.object("span", func(key string) error {
		var err error
		switch key {
		case "traceId":
			r.traceID, err = p.str(key)
		case "spanId":
			r.spanID, err = p.str(key)
		case "parentSpanId":
			r.parentID, err = p.str(key)
		case "name":
			r.name, err = p.str(key)
		case "startTimeUnixNano":
			r.start, err = p.unixNano(key)
		case "endTimeUnixNano":
			r.end, err = p.unixNano(key)
		case "attributes":
			// Counted across repeats of the key: a span cannot reset the cap
			// by listing "attributes" again.
			_, err = p.array("attributes", func(int) error {
				r.nAttrs++
				if r.nAttrs > MaxAttrsPerSpan {
					return fmt.Errorf("attributes: more than %d entries", MaxAttrsPerSpan)
				}
				if err := p.keyValue(r.attrs, 0); err != nil {
					return fmt.Errorf("attributes: %w", err)
				}
				return nil
			})
		case "events":
			// Counted across repeats of the key, like attributes.
			_, err = p.array("events", func(int) error {
				r.nEvents++
				// Only tool.output events are read. The rest, and everything
				// past the cap, are decoded (stdlib nesting cap) and dropped,
				// so a producer with many or odd events still converts.
				var raw json.RawMessage
				if err := p.dec.Decode(&raw); err != nil {
					return fmt.Errorf("events: %w", err)
				}
				if r.nEvents > maxEventsPerSpan || len(r.events) >= maxKeptEvents || eventName(raw) != EventToolOutput {
					return nil
				}
				ev, err := p.subParser(raw).event()
				var limit *limitError
				switch {
				case errors.As(err, &limit):
					return fmt.Errorf("events: %w", err)
				case err != nil:
					// A malformed tool.output event is dropped, not fatal;
					// trace check warns (malformed_event).
					r.droppedEvents++
				case ev.Name == EventToolOutput:
					r.events = append(r.events, ev)
				}
				return nil
			})
		case "status":
			_, err = p.object("status", func(key string) error {
				var err error
				switch key {
				case "code":
					r.statusError, err = p.statusCode()
				case "message":
					r.statusMessage, err = p.str("status.message")
				default:
					err = p.skip()
				}
				return err
			})
		default:
			err = p.skip()
		}
		return err
	})
	if err != nil {
		return Span{}, err
	}
	return r.validate()
}

// eventName is the name of a raw event, or "" when it is not an object with a
// string name.
func eventName(raw []byte) string {
	// Exact, case-sensitive key, like the strict event parser.
	var e map[string]json.RawMessage
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	var name string
	if json.Unmarshal(e["name"], &name) != nil {
		return ""
	}
	return name
}

// subParser reads one already-decoded value; it shares this parser's value
// count, so event attributes count toward the document-wide value cap.
func (p *otlpParser) subParser(raw []byte) *otlpSub {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return &otlpSub{p: &otlpParser{dec: d, values: p.values}, parent: p}
}

// otlpSub folds the sub-parser's value count back into its parent.
type otlpSub struct {
	p      *otlpParser
	parent *otlpParser
}

func (s *otlpSub) event() (Event, error) {
	ev, err := s.p.event()
	s.parent.values = s.p.values
	return ev, err
}

// event reads one span event. Its attributes are capped at MaxAttrsPerSpan
// per event and count toward the document-wide value cap.
func (p *otlpParser) event() (Event, error) {
	ev := Event{Attributes: map[string]any{}}
	n := 0
	_, err := p.object("event", func(key string) error {
		switch key {
		case "name":
			name, err := p.str("event.name")
			ev.Name = name
			return err
		case "attributes":
			_, err := p.array("event.attributes", func(int) error {
				n++
				if n > MaxAttrsPerSpan {
					return &limitError{fmt.Sprintf("event.attributes: more than %d entries", MaxAttrsPerSpan)}
				}
				return p.keyValue(ev.Attributes, 0)
			})
			return err
		}
		return p.skip()
	})
	return ev, err
}

func (r *rawSpan) validate() (Span, error) {
	traceID, err := hexID(r.traceID, traceIDHexLength, "traceId", false)
	if err != nil {
		return Span{}, err
	}
	spanID, err := hexID(r.spanID, spanIDHexLength, "spanId", false)
	if err != nil {
		return Span{}, err
	}
	parentID, err := hexID(r.parentID, spanIDHexLength, "parentSpanId", true)
	if err != nil {
		return Span{}, err
	}
	if r.start == nil {
		return Span{}, errors.New("startTimeUnixNano is required")
	}
	if r.end == nil {
		return Span{}, errors.New("endTimeUnixNano is required")
	}
	return Span{
		TraceID:       traceID,
		SpanID:        spanID,
		ParentSpanID:  parentID,
		Name:          r.name,
		StartNs:       *r.start,
		EndNs:         *r.end,
		Attributes:    r.attrs,
		Events:        r.events,
		DroppedEvents: r.droppedEvents,
		StatusError:   r.statusError,
		StatusMessage: r.statusMessage,
	}, nil
}

// hexID validates an OTLP/JSON id: lowercase or uppercase hex of the exact
// width, not all zeros. An empty or all-zero optional id returns "".
func hexID(v string, width int, field string, optional bool) (string, error) {
	if v == "" {
		if optional {
			return "", nil
		}
		return "", fmt.Errorf("%s is required", field)
	}
	if len(v) != width {
		return "", fmt.Errorf("%s must be %d hex characters, got %d", field, width, len(v))
	}
	v = strings.ToLower(v)
	allZero := true
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("%s is not hex: %s", field, quoteShort(v))
		}
		if c != '0' {
			allZero = false
		}
	}
	if allZero {
		if optional {
			return "", nil
		}
		return "", fmt.Errorf("%s is all zeros", field)
	}
	return v, nil
}

// unixNano reads a fixed64 timestamp, which OTLP/JSON writes as a decimal
// string and some producers write as a JSON number. Null is absent.
func (p *otlpParser) unixNano(field string) (*uint64, error) {
	t, err := p.token()
	if err != nil {
		return nil, err
	}
	var text string
	switch v := t.(type) {
	case nil:
		return nil, nil
	case string:
		text = v
	case json.Number:
		text = v.String()
	default:
		return nil, fmt.Errorf("%s must be an unsigned integer", field)
	}
	n, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s is not an unsigned integer: %s", field, quoteShort(text))
	}
	return &n, nil
}

// statusCode reads a status code as an integer or enum name and reports
// whether it is ERROR.
func (p *otlpParser) statusCode() (bool, error) {
	t, err := p.token()
	if err != nil {
		return false, err
	}
	switch v := t.(type) {
	case nil:
		return false, nil
	case string:
		return v == statusErrorString, nil
	case json.Number:
		n, err := strconv.ParseInt(v.String(), 10, 32)
		if err != nil {
			return false, fmt.Errorf("status.code is not an integer: %s", quoteShort(v.String()))
		}
		return n == otlpStatusError, nil
	}
	return false, errors.New("status.code must be an integer or enum name")
}

func (p *otlpParser) countValue() error {
	p.values++
	if p.values > MaxOTLPValues {
		return &limitError{fmt.Sprintf("OTLP input has more than %d attribute values", MaxOTLPValues)}
	}
	return nil
}

// keyValue reads one {key, value} entry into into. An entry whose value is
// null or empty is dropped.
func (p *otlpParser) keyValue(into map[string]any, depth int) error {
	if err := p.countValue(); err != nil {
		return err
	}
	var key string
	var val any
	_, err := p.object("attribute", func(field string) error {
		var err error
		switch field {
		case "key":
			key, err = p.str("key")
		case "value":
			val, err = p.anyValue(depth)
			if err != nil && key != "" {
				err = fmt.Errorf("%s: %w", quoteShort(key), err)
			}
		default:
			err = p.skip()
		}
		return err
	})
	if err != nil {
		return err
	}
	if key == "" {
		return errors.New("attribute with an empty key")
	}
	if val != nil {
		into[key] = val
	}
	return nil
}

// anyValue reads an OTLP AnyValue. An empty or null AnyValue is nil.
func (p *otlpParser) anyValue(depth int) (any, error) {
	if depth >= MaxAnyValueDepth {
		return nil, &limitError{fmt.Sprintf("value nesting exceeds %d levels", MaxAnyValueDepth)}
	}
	if err := p.countValue(); err != nil {
		return nil, err
	}
	var out any
	_, err := p.object("value", func(field string) error {
		v, err := p.anyValueField(field, depth)
		if err == nil && v != nil {
			out = v
		}
		return err
	})
	return out, err
}

func (p *otlpParser) anyValueField(field string, depth int) (any, error) {
	switch field {
	case "stringValue":
		t, err := p.token()
		if err != nil || t == nil {
			return nil, err
		}
		s, ok := t.(string)
		if !ok {
			return nil, errors.New("stringValue must be a string")
		}
		return s, nil
	case "boolValue":
		t, err := p.token()
		if err != nil || t == nil {
			return nil, err
		}
		b, ok := t.(bool)
		if !ok {
			return nil, errors.New("boolValue must be a boolean")
		}
		return b, nil
	case "intValue":
		return p.intValue()
	case "doubleValue":
		return p.doubleValue()
	case "bytesValue":
		t, err := p.token()
		if err != nil || t == nil {
			return nil, err
		}
		s, ok := t.(string)
		if !ok {
			return nil, errors.New("bytesValue must be a base64 string")
		}
		if _, err := base64.StdEncoding.DecodeString(s); err != nil {
			return nil, fmt.Errorf("bytesValue is not base64: %w", err)
		}
		return s, nil
	case "arrayValue":
		return p.arrayValue(depth)
	case "kvlistValue":
		return p.kvlistValue(depth)
	}
	return nil, p.skip()
}

func (p *otlpParser) arrayValue(depth int) (any, error) {
	var out []any
	found, err := p.object("arrayValue", func(field string) error {
		if field != "values" {
			return p.skip()
		}
		_, err := p.array("arrayValue.values", func(int) error {
			v, err := p.anyValue(depth + 1)
			if err != nil {
				return err
			}
			out = append(out, v)
			return nil
		})
		return err
	})
	if err != nil || !found {
		return nil, err
	}
	if out == nil {
		out = []any{}
	}
	return out, nil
}

func (p *otlpParser) kvlistValue(depth int) (any, error) {
	out := map[string]any{}
	found, err := p.object("kvlistValue", func(field string) error {
		if field != "values" {
			return p.skip()
		}
		_, err := p.array("kvlistValue.values", func(int) error {
			return p.keyValue(out, depth+1)
		})
		return err
	})
	if err != nil || !found {
		return nil, err
	}
	return out, nil
}

func (p *otlpParser) intValue() (any, error) {
	t, err := p.token()
	if err != nil || t == nil {
		return nil, err
	}
	var text string
	switch v := t.(type) {
	case string:
		text = v
	case json.Number:
		text = v.String()
	default:
		return nil, errors.New("intValue must be an integer")
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("intValue is not an int64: %s", quoteShort(text))
	}
	return n, nil
}

func (p *otlpParser) doubleValue() (any, error) {
	t, err := p.token()
	if err != nil || t == nil {
		return nil, err
	}
	n, ok := t.(json.Number)
	if !ok {
		return nil, errors.New("doubleValue must be a finite JSON number")
	}
	f, err := strconv.ParseFloat(n.String(), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("doubleValue is not a finite number: %s", quoteShort(n.String()))
	}
	return f, nil
}

// maxEchoBytes bounds how much of an offending input value an error repeats.
const maxEchoBytes = 64

// quoteShort quotes a value from the input for an error message, keeping at
// most maxEchoBytes of it so a huge value cannot flood stderr or an SDK
// exception.
func quoteShort(v string) string {
	if len(v) <= maxEchoBytes {
		return strconv.Quote(v)
	}
	cut := maxEchoBytes
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return fmt.Sprintf("%s… (%d bytes)", strconv.Quote(v[:cut]), len(v))
}
