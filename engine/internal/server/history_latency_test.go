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

	"github.com/attest-framework/attest/engine/pkg/types"
)

// TestEvaluateBatch_ThreeThousandSequentialCalls_EveryRequestUnderOneSecond
// drives 3,000 evaluate_batch requests on one assertion with history enabled
// and default pruning through the real handler. The synchronous quadratic
// prune stalled the engine near 2.8k evaluations; no request may take 1 s.
func TestEvaluateBatch_ThreeThousandSequentialCalls_EveryRequestUnderOneSecond(t *testing.T) {
	// Arrange
	t.Setenv("ATTEST_CACHE_DIR", t.TempDir())
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := New(stdinR, stdoutW, logger)
	if err := RegisterBuiltinHandlers(srv); err != nil {
		t.Fatalf("RegisterBuiltinHandlers: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(func() {
		cancel()
		stdinW.Close()
		stdoutR.Close()
	})
	go func() {
		_ = srv.Run(ctx)
		stdoutW.Close()
	}()
	reader := bufio.NewReader(stdoutR)

	call := func(id int64, method string, params any) types.Response {
		sendRequest(t, stdinW, id, method, params)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("request %d: read response: %v", id, err)
		}
		var resp types.Response
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("request %d: unmarshal response: %v", id, err)
		}
		if resp.Error != nil {
			t.Fatalf("request %d: unexpected error: %+v", id, resp.Error)
		}
		return resp
	}
	batch := types.EvaluateBatchParams{
		Trace: types.Trace{
			SchemaVersion: 1,
			TraceID:       "trace-latency",
			AgentID:       "agent",
			Input:         json.RawMessage(`"in"`),
			Output:        json.RawMessage(`{"message":"refund ok"}`),
			Steps:         []types.Step{},
		},
		Assertions: []types.Assertion{{
			AssertionID: "latency-content",
			Type:        types.TypeContent,
			Spec:        json.RawMessage(`{"target":"output.message","check":"contains","value":"refund ok"}`),
		}},
	}
	call(1, "initialize", initializeParams())

	// Act
	var slowest time.Duration
	slowestID := int64(0)
	for id := int64(2); id < 3002; id++ {
		start := time.Now()
		call(id, "evaluate_batch", batch)
		if d := time.Since(start); d > slowest {
			slowest, slowestID = d, id
		}
	}

	// Assert
	if slowest >= time.Second {
		t.Fatalf("slowest request %d took %s, want < 1s", slowestID, slowest)
	}
	t.Logf("slowest of 3000 requests: %s (request %d)", slowest, slowestID)
}
