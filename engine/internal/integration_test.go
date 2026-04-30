package internal_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/attest-ai/attest/engine/internal/server"
	"github.com/attest-ai/attest/engine/pkg/types"
)

func newE2EServer(t *testing.T) (io.WriteCloser, io.ReadCloser) {
	t.Helper()

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := server.New(stdinR, stdoutW, logger)
	if err := server.RegisterBuiltinHandlers(srv); err != nil {
		t.Fatalf("RegisterBuiltinHandlers: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(func() {
		cancel()
		stdinW.Close()
		stdoutR.Close()
	})

	go func() {
		_ = srv.Run(ctx)
		stdoutW.Close()
	}()

	return stdinW, stdoutR
}

func e2eSendRequest(t *testing.T, w io.Writer, id int64, method string, params any) {
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

func e2eReadResponse(t *testing.T, scanner *bufio.Scanner) *types.Response {
	t.Helper()
	if !scanner.Scan() {
		t.Fatalf("no response line: %v", scanner.Err())
	}
	var resp types.Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return &resp
}

func TestE2E_RefundAgentFullFlow(t *testing.T) {
	stdin, stdout := newE2EServer(t)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	// Step 1: initialize
	initParams := types.InitializeParams{
		SDKName:              "attest-test",
		SDKVersion:           "0.1.0",
		ProtocolVersion:      1,
		RequiredCapabilities: []string{"layers_1_4"},
		PreferredEncoding:    "json",
	}
	e2eSendRequest(t, stdin, 1, "initialize", initParams)
	initResp := e2eReadResponse(t, scanner)

	if initResp.Error != nil {
		t.Fatalf("initialize error: %+v", initResp.Error)
	}
	if initResp.ID != 1 {
		t.Errorf("initialize response ID = %d, want 1", initResp.ID)
	}

	var initResult types.InitializeResult
	if err := json.Unmarshal(initResp.Result, &initResult); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}
	if !initResult.Compatible {
		t.Fatalf("engine not compatible: missing=%v", initResult.Missing)
	}

	// Step 2: evaluate_batch with refund agent trace and L1–L4 assertions
	traceData, err := os.ReadFile("testdata/traces/e2e_refund_agent.json")
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}

	var trace types.Trace
	if err := json.Unmarshal(traceData, &trace); err != nil {
		t.Fatalf("unmarshal trace: %v", err)
	}

	assertions := []types.Assertion{
		{
			AssertionID: "assert_001",
			Type:        types.TypeSchema,
			RequestID:   "req_idempotency_key_001",
			Spec: mustMarshal(t, map[string]any{
				"target": "steps[?name=='lookup_order'].result",
				"schema": map[string]any{
					"type":     "object",
					"required": []string{"status", "amount"},
					"properties": map[string]any{
						"status": map[string]any{"type": "string"},
						"amount": map[string]any{"type": "number", "minimum": 0},
					},
				},
			}),
		},
		{
			AssertionID: "assert_002",
			Type:        types.TypeConstraint,
			RequestID:   "req_idempotency_key_002",
			Spec: mustMarshal(t, map[string]any{
				"field":    "metadata.cost_usd",
				"operator": "lte",
				"value":    0.01,
			}),
		},
		{
			AssertionID: "assert_003",
			Type:        types.TypeTrace,
			RequestID:   "req_idempotency_key_003",
			Spec: mustMarshal(t, map[string]any{
				"check": "contains_in_order",
				"tools": []string{"lookup_order", "process_refund"},
			}),
		},
		{
			AssertionID: "assert_004",
			Type:        types.TypeContent,
			RequestID:   "req_idempotency_key_004",
			Spec: mustMarshal(t, map[string]any{
				"target": "output.message",
				"check":  "contains",
				"value":  "refund",
			}),
		},
		{
			AssertionID: "assert_005",
			Type:        types.TypeContent,
			RequestID:   "req_idempotency_key_005",
			Spec: mustMarshal(t, map[string]any{
				"target": "output.message",
				"check":  "not_contains",
				"value":  "cannot process",
				"soft":   true,
			}),
		},
	}

	batchParams := types.EvaluateBatchParams{
		Trace:      trace,
		Assertions: assertions,
	}

	e2eSendRequest(t, stdin, 2, "evaluate_batch", batchParams)
	batchResp := e2eReadResponse(t, scanner)

	if batchResp.Error != nil {
		t.Fatalf("evaluate_batch error: %+v", batchResp.Error)
	}
	if batchResp.ID != 2 {
		t.Errorf("evaluate_batch response ID = %d, want 2", batchResp.ID)
	}

	var batchResult types.EvaluateBatchResult
	if err := json.Unmarshal(batchResp.Result, &batchResult); err != nil {
		t.Fatalf("unmarshal evaluate_batch result: %v", err)
	}

	if len(batchResult.Results) != len(assertions) {
		t.Fatalf("result count = %d, want %d", len(batchResult.Results), len(assertions))
	}

	// The engine must never mark its own results as simulated.
	if batchResult.Simulated {
		t.Errorf("Simulated = true on engine-produced result; want false")
	}

	// Verify all 5 assertions pass
	assertionIDs := map[string]bool{
		"assert_001": false,
		"assert_002": false,
		"assert_003": false,
		"assert_004": false,
		"assert_005": false,
	}
	for _, r := range batchResult.Results {
		if _, ok := assertionIDs[r.AssertionID]; !ok {
			t.Errorf("unexpected assertion_id %q in results", r.AssertionID)
			continue
		}
		assertionIDs[r.AssertionID] = true
		if r.Status != types.StatusPass {
			t.Errorf("assertion %s: status = %q, want %q; explanation: %s",
				r.AssertionID, r.Status, types.StatusPass, r.Explanation)
		}
		if r.Score != 1.0 {
			t.Errorf("assertion %s: score = %f, want 1.0", r.AssertionID, r.Score)
		}
	}
	for id, seen := range assertionIDs {
		if !seen {
			t.Errorf("assertion %s missing from results", id)
		}
	}

	// Step 3: shutdown
	e2eSendRequest(t, stdin, 99, "shutdown", map[string]any{})
	shutdownResp := e2eReadResponse(t, scanner)

	if shutdownResp.Error != nil {
		t.Fatalf("shutdown error: %+v", shutdownResp.Error)
	}
	if shutdownResp.ID != 99 {
		t.Errorf("shutdown response ID = %d, want 99", shutdownResp.ID)
	}

	var shutdownResult types.ShutdownResult
	if err := json.Unmarshal(shutdownResp.Result, &shutdownResult); err != nil {
		t.Fatalf("unmarshal shutdown result: %v", err)
	}
	if shutdownResult.AssertionsEvaluated < 5 {
		t.Errorf("assertions_evaluated = %d, want >= 5", shutdownResult.AssertionsEvaluated)
	}
	if shutdownResult.SessionsCompleted != 1 {
		t.Errorf("sessions_completed = %d, want 1", shutdownResult.SessionsCompleted)
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustMarshal: %v", err)
	}
	return b
}
