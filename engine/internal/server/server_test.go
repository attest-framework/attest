package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/attest-ai/attest/engine/internal/buildinfo"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// newTestServer creates a Server wired with built-in handlers and connected to in-memory pipes.
// Returns the write end of stdin, the read end of stdout, and the server.
// The server is started in a background goroutine; cancel via the returned context cancel.
func newTestServer(t *testing.T) (io.WriteCloser, io.ReadCloser, *Server) {
	t.Helper()

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := New(stdinR, stdoutW, logger)
	RegisterBuiltinHandlers(srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		stdinW.Close()
		stdoutR.Close()
	})

	go func() {
		_ = srv.Run(ctx)
		stdoutW.Close()
	}()

	return stdinW, stdoutR, srv
}

// sendRequest writes a JSON-RPC request as NDJSON.
func sendRequest(t *testing.T, w io.Writer, id int64, method string, params any) {
	t.Helper()
	p, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := types.Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  p,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

// readResponse reads one NDJSON line from r and unmarshals it into a Response.
func readResponse(t *testing.T, r io.Reader) *types.Response {
	t.Helper()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	if !scanner.Scan() {
		t.Fatalf("no response line: %v", scanner.Err())
	}
	var resp types.Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return &resp
}

func initializeParams() types.InitializeParams {
	return types.InitializeParams{
		SDKName:              "attest-test",
		SDKVersion:           "0.1.0",
		ProtocolVersion:      1,
		RequiredCapabilities: []string{"layers_1_4"},
		PreferredEncoding:    "json",
	}
}

func TestServer_Initialize(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	sendRequest(t, stdin, 1, "initialize", initializeParams())
	resp := readResponse(t, stdout)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want %q", resp.JSONRPC, "2.0")
	}
	if resp.ID != 1 {
		t.Errorf("ID = %d, want 1", resp.ID)
	}

	var result types.InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.EngineVersion != buildinfo.Version {
		t.Errorf("EngineVersion = %q, want %q", result.EngineVersion, buildinfo.Version)
	}
	if !result.Compatible {
		t.Errorf("Compatible = false, want true")
	}
	if len(result.Missing) != 0 {
		t.Errorf("Missing = %v, want []", result.Missing)
	}
}

func TestServer_InitializeTwice(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	sendRequest(t, stdin, 1, "initialize", initializeParams())
	_ = readResponse(t, stdout)

	sendRequest(t, stdin, 2, "initialize", initializeParams())
	resp := readResponse(t, stdout)

	if resp.Error == nil {
		t.Fatal("expected error on second initialize, got nil")
	}
	if resp.Error.Code != types.ErrSessionError {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, types.ErrSessionError)
	}
}

func TestServer_EvaluateBeforeInitialize(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	params := types.EvaluateBatchParams{
		Trace:      types.Trace{},
		Assertions: []types.Assertion{},
	}
	sendRequest(t, stdin, 1, "evaluate_batch", params)
	resp := readResponse(t, stdout)

	if resp.Error == nil {
		t.Fatal("expected SESSION_ERROR, got nil error")
	}
	if resp.Error.Code != types.ErrSessionError {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, types.ErrSessionError)
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	sendRequest(t, stdin, 1, "nonexistent_method", map[string]any{})
	resp := readResponse(t, stdout)

	if resp.Error == nil {
		t.Fatal("expected method_not_found error, got nil")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("Error.Code = %d, want -32601", resp.Error.Code)
	}
}

func TestServer_Shutdown(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	sendRequest(t, stdin, 1, "initialize", initializeParams())
	_ = readResponse(t, stdout)

	sendRequest(t, stdin, 2, "shutdown", map[string]any{})
	resp := readResponse(t, stdout)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	var result types.ShutdownResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal ShutdownResult: %v", err)
	}
	if result.SessionsCompleted != 1 {
		t.Errorf("SessionsCompleted = %d, want 1", result.SessionsCompleted)
	}
}

func TestServer_MalformedJSON(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	if _, err := io.WriteString(stdin, "not valid json\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := readResponse(t, stdout)

	if resp.Error == nil {
		t.Fatal("expected parse error, got nil")
	}
	if resp.Error.Code != -32700 {
		t.Errorf("Error.Code = %d, want -32700", resp.Error.Code)
	}
}

func TestServer_IncompatibleProtocolVersion(t *testing.T) {
	stdin, stdout, _ := newTestServer(t)

	params := types.InitializeParams{
		SDKName:              "attest-test",
		SDKVersion:           "0.1.0",
		ProtocolVersion:      99,
		RequiredCapabilities: []string{"layers_1_4"},
		PreferredEncoding:    "json",
	}
	sendRequest(t, stdin, 1, "initialize", params)
	resp := readResponse(t, stdout)

	if resp.Error == nil {
		t.Fatal("expected SESSION_ERROR for incompatible protocol version, got nil")
	}
	if resp.Error.Code != types.ErrSessionError {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, types.ErrSessionError)
	}
}
