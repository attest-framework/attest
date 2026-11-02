package capture

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func receiverServer(t *testing.T) (*Receiver, string) {
	t.Helper()
	r := &Receiver{}
	mux := http.NewServeMux()
	r.Register(mux)
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
