package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/attest-framework/attest/engine/pkg/types"
)

// TestEvaluateBatch_DriftTravelsInResult_NoUnsolicitedStdout drives real
// evaluate_batch requests over the stdio loop. A dynamic-threshold content
// assertion passes for ten runs to build history, then hard-fails. Every
// stdout line must be the response to the request with that id, and the
// failing response carries the drift report.
func TestEvaluateBatch_DriftTravelsInResult_NoUnsolicitedStdout(t *testing.T) {
	// Arrange
	t.Setenv("ATTEST_CACHE_DIR", t.TempDir())
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := New(stdinR, stdoutW, logger)
	if err := RegisterBuiltinHandlers(srv); err != nil {
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
	reader := bufio.NewReader(stdoutR)

	call := func(id int64, method string, params any) (map[string]json.RawMessage, types.Response) {
		sendRequest(t, stdinW, id, method, params)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("request %d: read line: %v", id, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatalf("request %d: unmarshal line %q: %v", id, line, err)
		}
		var resp types.Response
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("request %d: unmarshal response: %v", id, err)
		}
		return fields, resp
	}

	batch := func(output string) types.EvaluateBatchParams {
		return types.EvaluateBatchParams{
			Trace: types.Trace{
				SchemaVersion: 1,
				TraceID:       "trace-drift-" + output,
				AgentID:       "agent",
				Input:         json.RawMessage(`"in"`),
				Output:        json.RawMessage(`{"message":"` + output + `"}`),
				Steps:         []types.Step{},
			},
			Assertions: []types.Assertion{{
				AssertionID: "dyn-content",
				Type:        types.TypeContent,
				Spec:        json.RawMessage(`{"target":"output.message","check":"contains","value":"refund ok","threshold":"dynamic"}`),
			}},
		}
	}
	decode := func(id int64, resp types.Response) types.EvaluateBatchResult {
		if resp.Error != nil {
			t.Fatalf("request %d: unexpected error: %+v", id, resp.Error)
		}
		if string(resp.ID) != strconv.FormatInt(id, 10) {
			t.Fatalf("request %d: response id = %s", id, resp.ID)
		}
		var res types.EvaluateBatchResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("request %d: unmarshal result: %v", id, err)
		}
		return res
	}

	_, resp := call(1, "initialize", initializeParams())
	if resp.Error != nil {
		t.Fatalf("initialize: %+v", resp.Error)
	}

	// Act: ten passing runs build the baseline, then the failing request.
	var nextID int64 = 2
	for ; nextID < 12; nextID++ {
		fields, resp := call(nextID, "evaluate_batch", batch("refund ok"))
		if _, hasMethod := fields["method"]; hasMethod {
			t.Fatalf("request %d: stdout line is a notification: %v", nextID, fields)
		}
		res := decode(nextID, resp)
		if res.Results[0].Status != types.StatusPass || res.Results[0].Drift != nil {
			t.Fatalf("request %d: want pass without drift, got %+v", nextID, res.Results[0])
		}
	}
	failID := nextID
	fields, resp := call(failID, "evaluate_batch", batch("double refund"))
	res := decode(failID, resp)

	// Assert (a): the very next line was the failing request's own response.
	if _, hasMethod := fields["method"]; hasMethod {
		t.Fatalf("stdout line for request %d is a notification: %v", failID, fields)
	}
	if string(fields["id"]) != "12" {
		t.Fatalf("id = %s, want 12", fields["id"])
	}
	// Assert (b): drift is carried in the result.
	got := res.Results[0]
	if got.Status != types.StatusHardFail {
		t.Fatalf("Status = %q, want hard_fail", got.Status)
	}
	if got.Drift == nil {
		t.Fatal("result.drift missing on hard-failed dynamic assertion")
	}
	if got.Drift.AssertionID != "dyn-content" || got.Drift.Status != "drift_detected" ||
		got.Drift.Count != 11 || got.Drift.Mean <= 0.9 || got.Drift.LatestScore != 0 ||
		got.Drift.Deviation != got.Drift.LatestScore-got.Drift.Mean {
		t.Errorf("unexpected drift report: %+v", got.Drift)
	}

	// No further line may be pending: shutdown's response is the next one.
	if _, resp := call(failID+1, "shutdown", struct{}{}); string(resp.ID) != strconv.FormatInt(failID+1, 10) {
		t.Errorf("line after the failing batch has id %s, want %d", resp.ID, failID+1)
	}
}
