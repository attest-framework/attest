package capture

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/attest-ai/attest/engine/internal/trace"
)

func receiverServer(t *testing.T) (*Receiver, string) {
	t.Helper()
	r := &Receiver{}
	mux := http.NewServeMux()
	r.Register(mux, "")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return r, srv.URL + TracesPath
}

func oneSpan() []*tracepb.Span {
	return []*tracepb.Span{span(randHex(16), randHex(8), "", "invoke_agent a", 1, 2, kv("gen_ai.operation.name", "invoke_agent"))}
}

func TestReceiver_GzipProtobuf_Accepted(t *testing.T) {
	r, url := receiverServer(t)
	body, _ := proto.Marshal(exportRequest(oneSpan()))
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write(body)
	zw.Close()
	req, _ := http.NewRequest(http.MethodPost, url, &zb)
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK || len(r.Spans()) != 1 || len(r.Spans()[0].TraceID) != 32 {
		t.Fatalf("status %d spans %+v", resp.StatusCode, r.Spans())
	}
}

func TestReceiver_RejectsBadRequests(t *testing.T) {
	r, url := receiverServer(t)
	for _, c := range []struct {
		name, method, contentType, encoding, body string
		want                                      int
	}{
		{"get", http.MethodGet, "application/json", "", "", http.StatusMethodNotAllowed},
		{"text", http.MethodPost, "text/plain", "", "{}", http.StatusUnsupportedMediaType},
		{"brotli", http.MethodPost, "application/json", "br", "{}", http.StatusUnsupportedMediaType},
		{"bad json", http.MethodPost, "application/json", "", "{", http.StatusBadRequest},
		{"bad protobuf", http.MethodPost, "application/x-protobuf", "", "\xff\xff\xff", http.StatusBadRequest},
		{"bad gzip", http.MethodPost, "application/json", "gzip", "nope", http.StatusBadRequest},
	} {
		req, _ := http.NewRequest(c.method, url, strings.NewReader(c.body))
		req.Header.Set("Content-Type", c.contentType)
		if c.encoding != "" {
			req.Header.Set("Content-Encoding", c.encoding)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if n := len(r.Spans()); n != 0 {
		t.Fatalf("kept %d spans from bad requests", n)
	}
}

// attrBomb is an ExportTraceServiceRequest of `spans` spans, each with
// `attrs` empty attributes, built as raw protobuf.
func attrBomb(spans, attrs int) []byte {
	span := bytes.Repeat([]byte{0x4a, 0x00}, attrs) // field 9 (attributes), length 0
	var scope []byte
	for i := 0; i < spans; i++ {
		scope = protowire.AppendTag(scope, 2, protowire.BytesType)
		scope = protowire.AppendBytes(scope, span)
	}
	var rs []byte
	rs = protowire.AppendTag(rs, 2, protowire.BytesType)
	rs = protowire.AppendBytes(rs, scope)
	var req []byte
	req = protowire.AppendTag(req, 1, protowire.BytesType)
	return protowire.AppendBytes(req, rs)
}

func postGzipProtobuf(t *testing.T, url string, body []byte) int {
	t.Helper()
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write(body)
	zw.Close()
	req, _ := http.NewRequest(http.MethodPost, url, &zb)
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestReceiver_GzipAttributeBomb_RejectedBeforeDecoding(t *testing.T) {
	r, url := receiverServer(t)
	body := attrBomb(1500, 1000) // 1.5M attributes in ~3 MB
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()

	status := postGzipProtobuf(t, url, body)

	runtime.ReadMemStats(&after)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", status)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Errorf("allocated %d MiB; the body must not be decoded", alloc>>20)
	}
	if len(r.Spans()) != 0 {
		t.Fatal("kept spans from a rejected body")
	}
}

func TestReceiver_SpanOverAttributeCap_413(t *testing.T) {
	_, url := receiverServer(t)

	if status := postGzipProtobuf(t, url, attrBomb(1, trace.MaxAttrsPerSpan+1)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", status)
	}
}
