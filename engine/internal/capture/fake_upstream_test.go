package capture

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUpstream speaks OpenAI Chat Completions and Responses under /v1 and
// Anthropic Messages under /v1/messages, streaming and not. Ids and created
// timestamps change on every response, as a real provider's do. A streaming
// response sends its first event, then waits for release before the rest,
// so tests can prove events reach the client before the stream ends.
type fakeUpstream struct {
	srv     *httptest.Server
	release chan struct{}
	// canceled receives when a streaming request is dropped mid-stream.
	canceled chan struct{}
	seq      atomic.Int64
	// auth holds the last Authorization or x-api-key header seen.
	auth atomic.Value
	// deltas, when set, replace every streamed response's text deltas.
	deltas []string
}

func (f *fakeUpstream) deltasOr(defaults ...string) []string {
	if f.deltas != nil {
		return f.deltas
	}
	return defaults
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{release: make(chan struct{}, 16), canceled: make(chan struct{}, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// OpenAIBase and AnthropicBase are the upstream base URLs as the SDKs
// configure them.
func (f *fakeUpstream) OpenAIBase() string    { return f.srv.URL + "/v1" }
func (f *fakeUpstream) AnthropicBase() string { return f.srv.URL }

func (f *fakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	if a := r.Header.Get("Authorization"); a != "" {
		f.auth.Store(a)
	} else if k := r.Header.Get("X-Api-Key"); k != "" {
		f.auth.Store(k)
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	n := f.seq.Add(1)
	nonce := fmt.Sprintf("%d%d", time.Now().UnixNano(), n)
	created := time.Now().Unix() + n
	var events []string
	var whole any
	switch r.URL.Path {
	case "/v1/chat/completions":
		id := "chatcmpl-" + nonce
		whole = map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": req.Model,
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "hello from chat"}}},
		}
		for _, d := range f.deltasOr("hello ", "from ", "chat") {
			events = append(events, "data: "+mustJSON(map[string]any{"id": id, "object": "chat.completion.chunk", "created": created,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": d}}}})+"\n\n")
		}
		events = append(events, "data: [DONE]\n\n")
	case "/v1/responses":
		id, msg := "resp_"+nonce, "msg_"+nonce
		whole = map[string]any{
			"id": id, "object": "response", "created_at": created, "model": req.Model, "status": "completed",
			"output": []any{map[string]any{"type": "message", "id": msg, "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "hello from responses"}}}},
		}
		events = append(events, "event: response.created\ndata: "+mustJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": id, "created_at": created}})+"\n\n")
		for _, d := range f.deltasOr("hello ", "from ", "responses") {
			events = append(events, "event: response.output_text.delta\ndata: "+mustJSON(map[string]any{"type": "response.output_text.delta", "item_id": msg, "delta": d})+"\n\n")
		}
		events = append(events, "event: response.completed\ndata: "+mustJSON(map[string]any{"type": "response.completed", "response": whole})+"\n\n")
	case "/v1/messages":
		id := "msg_" + nonce
		whole = map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": req.Model, "stop_reason": "end_turn",
			"content": []any{map[string]any{"type": "text", "text": "hello from messages"}},
		}
		events = append(events, "event: message_start\ndata: "+mustJSON(map[string]any{"type": "message_start", "message": map[string]any{"id": id, "type": "message", "content": []any{}}})+"\n\n")
		for _, d := range f.deltasOr("hello ", "from ", "messages") {
			events = append(events, "event: content_block_delta\ndata: "+mustJSON(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": d}})+"\n\n")
		}
		events = append(events, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	default:
		http.NotFound(w, r)
		return
	}
	if !req.Stream {
		f.writeJSON(w, r, whole)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl := w.(http.Flusher)
	_, _ = io.WriteString(w, events[0])
	fl.Flush()
	select {
	case <-f.release:
	case <-r.Context().Done():
		// The proxy dropped the upstream request: its client went away.
		f.canceled <- struct{}{}
		return
	case <-time.After(5 * time.Second):
	}
	for _, e := range events[1:] {
		_, _ = io.WriteString(w, e)
		fl.Flush()
	}
}

// writeJSON gzips the body when the client accepts it, as providers do.
func (f *fakeUpstream) writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		_, _ = io.WriteString(w, mustJSON(v))
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	zw := gzip.NewWriter(w)
	_, _ = io.WriteString(zw, mustJSON(v))
	_ = zw.Close()
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
