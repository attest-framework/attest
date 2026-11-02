package capture

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// plantedKey and plantedEmail must never reach a fixture.
const (
	plantedKey   = "sk-test-planted0123456789abcdefABCDEF"
	plantedEmail = "jane.doe@example.com"
)

// scriptedRun plays one agent run against a capture's environment: three
// model calls (Chat Completions, streamed Responses, streamed Anthropic
// Messages) through the proxy, then its spans to the receiver, half as
// OTLP/JSON and half as http/protobuf. Ids and timestamps are fresh on every
// run. issueRefunds sets how often issue_refund runs for the same order.
func scriptedRun(t *testing.T, env map[string]string, up *fakeUpstream, issueRefunds int) {
	t.Helper()
	tools := `[{"type":"function","function":{"name":"issue_refund","parameters":{"type":"object","properties":{"order_id":{"type":"string"}}}}}]`
	modelCall(t, env["OPENAI_BASE_URL"]+"/chat/completions", up, false,
		`{"model":"gpt-test","temperature":0,"user":"`+plantedKey+`","tools":`+tools+`,"messages":[{"role":"user","content":"Refund order O-1 for `+plantedEmail+`"}]}`)
	modelCall(t, env["OPENAI_BASE_URL"]+"/responses", up, true,
		`{"model":"gpt-test","stream":true,"input":"Summarize the refund for `+plantedEmail+`"}`)
	modelCall(t, env["ANTHROPIC_BASE_URL"]+"/v1/messages", up, true,
		`{"model":"claude-test","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Write the customer note"}]}`)

	traceID, root := randHex(16), randHex(8)
	start := time.Now().UnixNano()
	at := func(i int) uint64 { return uint64(start + int64(i)*int64(time.Millisecond)) }
	spans := []*tracepb.Span{
		span(traceID, root, "", "invoke_agent refund-desk", at(0), at(20), kv("gen_ai.operation.name", "invoke_agent"), kv("gen_ai.agent.name", "refund-desk"),
			kv("refund_desk.debug", "using key "+plantedKey)),
		span(traceID, randHex(8), root, "chat gpt-test", at(1), at(2), kv("gen_ai.operation.name", "chat"), kv("gen_ai.request.model", "gpt-test")),
		toolSpan(traceID, root, "lookup_order", `{"order_id":"O-1"}`, at(3), at(4)),
	}
	for i := 0; i < issueRefunds; i++ {
		spans = append(spans, toolSpan(traceID, root, "issue_refund", `{"order_id":"O-1","email":"`+plantedEmail+`"}`, at(5+2*i), at(6+2*i)))
	}
	half := len(spans) / 2
	postProtobuf(t, env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"], spans[half:])
	postJSON(t, env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"], spans[:half])
}

func modelCall(t *testing.T, url string, up *fakeUpstream, stream bool, body string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+plantedKey)
	req.Header.Set("X-Api-Key", plantedKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: %d %s", url, resp.StatusCode, b)
	}
	if !stream {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	// The first event must arrive while the upstream still holds the rest.
	br := bufio.NewReader(resp.Body)
	first, err := readEvent(br)
	if err != nil || first == "" {
		t.Fatalf("POST %s: first event: %q %v", url, first, err)
	}
	up.release <- struct{}{}
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	return first + string(rest)
}

func readEvent(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		line, err := br.ReadString('\n')
		sb.WriteString(line)
		if err != nil {
			return sb.String(), err
		}
		if line == "\n" {
			return sb.String(), nil
		}
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func kv(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func span(traceID, spanID, parent, name string, start, end uint64, attrs ...*commonpb.KeyValue) *tracepb.Span {
	tid, _ := hex.DecodeString(traceID)
	sid, _ := hex.DecodeString(spanID)
	s := &tracepb.Span{TraceId: tid, SpanId: sid, Name: name, StartTimeUnixNano: start, EndTimeUnixNano: end, Attributes: attrs}
	if parent != "" {
		s.ParentSpanId, _ = hex.DecodeString(parent)
	}
	return s
}

func toolSpan(traceID, parent, tool, args string, start, end uint64) *tracepb.Span {
	return span(traceID, randHex(8), parent, "execute_tool "+tool, start, end,
		kv("gen_ai.operation.name", "execute_tool"), kv("gen_ai.tool.name", tool),
		kv("gen_ai.tool.call.id", "call_"+randHex(12)), kv("gen_ai.tool.call.arguments", args),
		kv("gen_ai.tool.call.result", `{"ok":true}`))
}

// exportRequest is an ExportTraceServiceRequest, encoded through its
// wire-compatible TracesData form.
func exportRequest(spans []*tracepb.Span) *tracepb.TracesData {
	return &tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}}}
}

func postProtobuf(t *testing.T, url string, spans []*tracepb.Span) {
	t.Helper()
	body, err := proto.Marshal(exportRequest(spans))
	if err != nil {
		t.Fatal(err)
	}
	postOTLP(t, url, "application/x-protobuf", body)
}

func postJSON(t *testing.T, url string, spans []*tracepb.Span) {
	t.Helper()
	pb, err := proto.Marshal(exportRequest(spans))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := protobufToOTLPJSON(pb)
	if err != nil {
		t.Fatal(err)
	}
	postOTLP(t, url, "application/json", doc)
}

func postOTLP(t *testing.T, url, contentType string, body []byte) {
	t.Helper()
	resp, err := http.Post(url, contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("OTLP %s: %d %s", contentType, resp.StatusCode, b)
	}
}

// captureRun runs scriptedRun under a fresh session and builds its fixture.
func captureRun(t *testing.T, up *fakeUpstream, issueRefunds int, expectFail bool) (*Fixture, []byte) {
	t.Helper()
	s, err := Start(Upstreams{OpenAI: up.OpenAIBase(), Anthropic: up.AnthropicBase()})
	if err != nil {
		t.Fatal(err)
	}
	scriptedRun(t, s.Env(), up, issueRefunds)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	spans, exchanges, incomplete := s.Recorded()
	if incomplete != 0 {
		t.Fatalf("incomplete exchanges: %d", incomplete)
	}
	f, err := BuildFixture(BuildInput{
		Name:       "scripted",
		Source:     Source{Kind: SourceCommand, ReplayCapable: true, Command: []string{"agent", "--key", plantedKey}},
		Spans:      spans,
		Exchanges:  exchanges,
		ExpectFail: expectFail,
		Redactor:   redactorFor(plantedKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return f, data
}

func describe(f *Fixture) string {
	var sb strings.Builder
	for _, r := range f.Verification.Results {
		fmt.Fprintf(&sb, "%s=%s; ", r.Contract, r.Status)
	}
	return sb.String()
}
