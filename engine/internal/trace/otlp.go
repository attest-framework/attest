package trace

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// OTLP/JSON input limits. Input is untrusted (exported by any producer), so
// every dimension is bounded before conversion.
const (
	MaxOTLPBytes      = 32 << 20 // 32 MiB
	MaxOTLPSpans      = 20000
	MaxAnyValueDepth  = 32
	maxAttrsPerSpan   = 1024
	otlpStatusError   = 2
	traceIDHexLength  = 32
	spanIDHexLength   = 16
	statusErrorString = "STATUS_CODE_ERROR"
)

// Span is one OTLP span reduced to the fields trace conversion and trace
// check read. IDs are lowercase hex. Attribute values are string, bool,
// int64, float64, []any or map[string]any.
type Span struct {
	TraceID       string
	SpanID        string
	ParentSpanID  string
	Name          string
	StartNs       uint64
	EndNs         uint64
	Attributes    map[string]any
	StatusError   bool
	StatusMessage string
	// Order is the span's position in the input document.
	Order int
}

type otlpDocument struct {
	ResourceSpans *[]otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpScopeSpans struct {
	Spans []otlpSpan `json:"spans"`
}

type otlpSpan struct {
	TraceID      string          `json:"traceId"`
	SpanID       string          `json:"spanId"`
	ParentSpanID string          `json:"parentSpanId"`
	Name         string          `json:"name"`
	Start        json.RawMessage `json:"startTimeUnixNano"`
	End          json.RawMessage `json:"endTimeUnixNano"`
	Attributes   []otlpKeyValue  `json:"attributes"`
	Status       *otlpStatus     `json:"status"`
}

type otlpStatus struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
}

type otlpKeyValue struct {
	Key   string        `json:"key"`
	Value *otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue *string         `json:"stringValue"`
	BoolValue   *bool           `json:"boolValue"`
	IntValue    json.RawMessage `json:"intValue"`
	DoubleValue json.RawMessage `json:"doubleValue"`
	BytesValue  *string         `json:"bytesValue"`
	ArrayValue  *struct {
		Values []otlpAnyValue `json:"values"`
	} `json:"arrayValue"`
	KvlistValue *struct {
		Values []otlpKeyValue `json:"values"`
	} `json:"kvlistValue"`
}

// ParseOTLP decodes an OTLP/JSON trace export (ExportTraceServiceRequest:
// resourceSpans → scopeSpans → spans). Malformed or oversized input returns
// an error naming the offending location; it never panics.
func ParseOTLP(data []byte) ([]Span, error) {
	if len(data) > MaxOTLPBytes {
		return nil, fmt.Errorf("OTLP input is %d bytes; the limit is %d", len(data), MaxOTLPBytes)
	}
	var doc otlpDocument
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("OTLP input is not valid OTLP/JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("OTLP input has trailing data after the JSON document")
	}
	if doc.ResourceSpans == nil {
		return nil, errors.New("OTLP input has no resourceSpans array")
	}
	var spans []Span
	for ri, rs := range *doc.ResourceSpans {
		for si, ss := range rs.ScopeSpans {
			for pi := range ss.Spans {
				if len(spans) >= MaxOTLPSpans {
					return nil, fmt.Errorf("OTLP input has more than %d spans", MaxOTLPSpans)
				}
				loc := fmt.Sprintf("resourceSpans[%d].scopeSpans[%d].spans[%d]", ri, si, pi)
				span, err := convertSpan(&ss.Spans[pi], len(spans))
				if err != nil {
					return nil, fmt.Errorf("%s: %w", loc, err)
				}
				spans = append(spans, span)
			}
		}
	}
	if len(spans) == 0 {
		return nil, errors.New("OTLP input contains no spans")
	}
	return spans, nil
}

func convertSpan(s *otlpSpan, order int) (Span, error) {
	traceID, err := hexID(s.TraceID, traceIDHexLength, "traceId", false)
	if err != nil {
		return Span{}, err
	}
	spanID, err := hexID(s.SpanID, spanIDHexLength, "spanId", false)
	if err != nil {
		return Span{}, err
	}
	parentID, err := hexID(s.ParentSpanID, spanIDHexLength, "parentSpanId", true)
	if err != nil {
		return Span{}, err
	}
	start, err := unixNano(s.Start, "startTimeUnixNano")
	if err != nil {
		return Span{}, err
	}
	end, err := unixNano(s.End, "endTimeUnixNano")
	if err != nil {
		return Span{}, err
	}
	if len(s.Attributes) > maxAttrsPerSpan {
		return Span{}, fmt.Errorf("attributes: %d entries; the limit is %d", len(s.Attributes), maxAttrsPerSpan)
	}
	attrs, err := keyValues(s.Attributes, 0)
	if err != nil {
		return Span{}, fmt.Errorf("attributes: %w", err)
	}
	out := Span{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentID,
		Name:         s.Name,
		StartNs:      start,
		EndNs:        end,
		Attributes:   attrs,
		Order:        order,
	}
	if s.Status != nil {
		isErr, err := statusIsError(s.Status.Code)
		if err != nil {
			return Span{}, err
		}
		out.StatusError = isErr
		out.StatusMessage = s.Status.Message
	}
	return out, nil
}

// hexID validates an OTLP/JSON id: lowercase or uppercase hex of the exact
// width, not all zeros. An empty optional id returns "".
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
			return "", fmt.Errorf("%s is not hex: %q", field, v)
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

// unixNano parses a fixed64 timestamp, which OTLP/JSON writes as a decimal
// string and some producers write as a JSON number.
func unixNano(raw json.RawMessage, field string) (uint64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, fmt.Errorf("%s is required", field)
	}
	text := string(raw)
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, fmt.Errorf("%s: %w", field, err)
		}
	}
	n, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is not an unsigned integer: %s", field, raw)
	}
	return n, nil
}

func statusIsError(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return false, fmt.Errorf("status.code: %w", err)
		}
		return s == statusErrorString, nil
	}
	n, err := strconv.ParseInt(string(raw), 10, 32)
	if err != nil {
		return false, fmt.Errorf("status.code is not an integer or enum name: %s", raw)
	}
	return n == otlpStatusError, nil
}

func keyValues(kvs []otlpKeyValue, depth int) (map[string]any, error) {
	out := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		if kv.Key == "" {
			return nil, errors.New("attribute with an empty key")
		}
		if kv.Value == nil {
			continue
		}
		v, err := anyValue(kv.Value, depth)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", kv.Key, err)
		}
		if v != nil {
			out[kv.Key] = v
		}
	}
	return out, nil
}

// anyValue converts an OTLP AnyValue. An empty AnyValue (no field set) is
// nil, which the caller drops.
func anyValue(v *otlpAnyValue, depth int) (any, error) {
	if depth >= MaxAnyValueDepth {
		return nil, fmt.Errorf("value nesting exceeds %d levels", MaxAnyValueDepth)
	}
	switch {
	case v.StringValue != nil:
		return *v.StringValue, nil
	case v.BoolValue != nil:
		return *v.BoolValue, nil
	case len(v.IntValue) > 0 && string(v.IntValue) != "null":
		return intValue(v.IntValue)
	case len(v.DoubleValue) > 0 && string(v.DoubleValue) != "null":
		return doubleValue(v.DoubleValue)
	case v.BytesValue != nil:
		if _, err := base64.StdEncoding.DecodeString(*v.BytesValue); err != nil {
			return nil, fmt.Errorf("bytesValue is not base64: %w", err)
		}
		return *v.BytesValue, nil
	case v.ArrayValue != nil:
		out := make([]any, 0, len(v.ArrayValue.Values))
		for i := range v.ArrayValue.Values {
			item, err := anyValue(&v.ArrayValue.Values[i], depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	case v.KvlistValue != nil:
		return keyValues(v.KvlistValue.Values, depth+1)
	}
	return nil, nil
}

func intValue(raw json.RawMessage) (int64, error) {
	text := string(raw)
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, fmt.Errorf("intValue: %w", err)
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("intValue is not an int64: %s", raw)
	}
	return n, nil
}

func doubleValue(raw json.RawMessage) (float64, error) {
	if raw[0] == '"' {
		return 0, fmt.Errorf("doubleValue must be a finite JSON number: %s", raw)
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("doubleValue is not a finite number: %s", raw)
	}
	return f, nil
}
