package capture

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/attest-ai/attest/engine/internal/trace"
)

// TracesPath is the OTLP/HTTP traces endpoint path.
const TracesPath = "/v1/traces"

// Receiver is an OTLP/HTTP trace receiver. It accepts http/protobuf (the
// Python exporter's only encoding) and http/json, optionally gzip-encoded,
// and keeps every span it is sent. Input is untrusted: each request is
// bounded by the OTLP/JSON parser's limits, and the total span count by
// trace.MaxOTLPSpans.
type Receiver struct {
	mu    sync.Mutex
	spans []trace.Span
}

// Spans returns every span received so far, numbered in arrival order.
func (r *Receiver) Spans() []trace.Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]trace.Span(nil), r.spans...)
}

// Register adds the traces endpoint to mux.
func (r *Receiver) Register(mux *http.ServeMux) {
	mux.HandleFunc(TracesPath, r.serve)
}

func (r *Receiver) serve(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "attest capture: POST only", http.StatusMethodNotAllowed)
		return
	}
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && mediaType != "application/x-protobuf") {
		http.Error(w, "attest capture: Content-Type must be application/json or application/x-protobuf", http.StatusUnsupportedMediaType)
		return
	}
	body, status, err := readOTLPBody(req)
	if err != nil {
		http.Error(w, "attest capture: "+err.Error(), status)
		return
	}
	doc := body
	if mediaType == "application/x-protobuf" {
		if doc, err = protobufToOTLPJSON(body); err != nil {
			http.Error(w, "attest capture: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	spans, err := trace.ParseOTLP(doc)
	if err != nil {
		http.Error(w, "attest capture: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := r.add(spans); err != nil {
		http.Error(w, "attest capture: "+err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	// An empty ExportTraceServiceResponse: zero bytes in protobuf, {} in JSON.
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(http.StatusOK)
	if mediaType == "application/json" {
		_, _ = io.WriteString(w, "{}")
	}
}

func (r *Receiver) add(spans []trace.Span) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.spans)+len(spans) > trace.MaxOTLPSpans {
		return fmt.Errorf("capture holds more than %d spans", trace.MaxOTLPSpans)
	}
	for _, s := range spans {
		s.Order = len(r.spans)
		r.spans = append(r.spans, s)
	}
	return nil
}

// readOTLPBody reads a request body, decoding gzip, bounded by the OTLP size
// limit after decoding.
func readOTLPBody(req *http.Request) ([]byte, int, error) {
	var src io.Reader = req.Body
	switch enc := strings.ToLower(req.Header.Get("Content-Encoding")); enc {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(io.LimitReader(req.Body, trace.MaxOTLPBytes+1))
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("gzip body: %w", err)
		}
		defer zr.Close()
		src = zr
	default:
		return nil, http.StatusUnsupportedMediaType, fmt.Errorf("unsupported Content-Encoding %q", enc)
	}
	body, err := io.ReadAll(io.LimitReader(src, trace.MaxOTLPBytes+1))
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("reading body: %w", err)
	}
	if len(body) > trace.MaxOTLPBytes {
		return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("body exceeds %d bytes", trace.MaxOTLPBytes)
	}
	return body, 0, nil
}

// protobufToOTLPJSON converts an http/protobuf ExportTraceServiceRequest to
// OTLP/JSON. It decodes the body as TracesData, which OTLP defines to be
// wire-compatible with ExportTraceServiceRequest, so the receiver does not
// link the gRPC collector service. protojson encodes bytes fields as base64,
// while OTLP/JSON requires trace and span ids as hex, so the ids are
// re-encoded.
func protobufToOTLPJSON(body []byte) ([]byte, error) {
	var msg tracepb.TracesData
	if err := proto.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("protobuf body: %w", err)
	}
	raw, err := protojson.MarshalOptions{UseEnumNumbers: true}.Marshal(&msg)
	if err != nil {
		return nil, fmt.Errorf("protobuf body: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("protobuf body: %w", err)
	}
	for _, rs := range asSlice(doc["resourceSpans"]) {
		for _, ss := range asSlice(asMap(rs)["scopeSpans"]) {
			for _, sp := range asSlice(asMap(ss)["spans"]) {
				span := asMap(sp)
				for _, k := range []string{"traceId", "spanId", "parentSpanId"} {
					v, ok := span[k].(string)
					if !ok {
						continue
					}
					id, err := base64.StdEncoding.DecodeString(v)
					if err != nil {
						return nil, fmt.Errorf("protobuf body: span %s: %w", k, err)
					}
					span[k] = hex.EncodeToString(id)
				}
			}
		}
	}
	return json.Marshal(doc)
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
