// Package mcp serves attest to coding agents over the Model Context Protocol.
//
// The server is read-only and offline: it writes no files, opens no sockets
// and makes no model calls. Every path an agent supplies is resolved inside
// the project root. The toolset is fixed at startup.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/attest-framework/attest/engine/internal/assertion"
	"github.com/attest-framework/attest/engine/internal/pathguard"
)

// Limits on what one call may carry or read.
const (
	// MaxFileBytes caps every file the server reads.
	MaxFileBytes = 32 << 20
	// MaxArgumentBytes caps the arguments of one tool call.
	MaxArgumentBytes = 16 << 20
)

const instructions = "attest checks an agent's recorded behavior without calling a model. " +
	"fixture_info and draft_contract start from a capture fixture; run_contract iterates contract statements against it; " +
	"evaluate checks raw assertion specs; explain_failure reads a report. " +
	"Read the guide with `attest guide mcp`."

const (
	// callTimeout bounds one tool call.
	callTimeout = 30 * time.Second
	// maxConcurrentCalls bounds tool calls running at once.
	maxConcurrentCalls = 4
)

// env is what the tool handlers share. It holds no per-call state.
type env struct {
	root     *pathguard.Root
	pipeline *assertion.Pipeline
	slots    chan struct{}
}

func newEnv(root *pathguard.Root, pipeline *assertion.Pipeline) *env {
	return &env{root: root, pipeline: pipeline, slots: make(chan struct{}, maxConcurrentCalls)}
}

// NewServer builds the server with its fixed toolset. Logs go to logger.
func NewServer(root *pathguard.Root, version string, logger *slog.Logger) *mcp.Server {
	return newServer(newEnv(root, assertion.NewPipeline(assertion.NewRegistry())), version, logger)
}

func newServer(e *env, version string, logger *slog.Logger) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "attest", Version: version}, &mcp.ServerOptions{
		Instructions: instructions,
		Logger:       logger,
	})
	for i := range tools {
		def := tools[i]
		srv.AddTool(&mcp.Tool{
			Name:         def.Name,
			Description:  def.Description,
			InputSchema:  def.InputSchema,
			OutputSchema: def.OutputSchema,
		}, e.handler(def))
	}
	return srv
}

// Serve runs the server on stdin and stdout until the client disconnects or
// ctx ends. Only protocol frames go to stdout; logs go to logs.
func Serve(ctx context.Context, root *pathguard.Root, version string, logs io.Writer) error {
	logger := slog.New(slog.NewTextHandler(logs, nil))
	return NewServer(root, version, logger).Run(ctx, &mcp.StdioTransport{})
}

// handler runs one tool call with a deadline and a concurrency slot. A panic
// in the call becomes a tool error; a call past its deadline is abandoned (its
// goroutine ends when the work does).
func (e *env) handler(def toolDef) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.Params.Arguments
		if len(args) > MaxArgumentBytes {
			return toolError(fmt.Errorf("arguments are larger than %d bytes", MaxArgumentBytes)), nil
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		select {
		case e.slots <- struct{}{}:
		case <-ctx.Done():
			return toolError(errors.New("the server is busy; try again")), nil
		}
		type outcome struct {
			out any
			err error
		}
		done := make(chan outcome, 1)
		go func() {
			defer func() { <-e.slots }()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("tool call panicked", "tool", def.Name, "panic", fmt.Sprint(r))
					done <- outcome{err: errors.New("internal error while running the tool")}
				}
			}()
			out, err := def.run(e, ctx, args)
			done <- outcome{out, err}
		}()
		var res outcome
		select {
		case res = <-done:
		case <-ctx.Done():
			return toolError(fmt.Errorf("the call took longer than %s", callTimeout)), nil
		}
		if res.err != nil {
			return toolError(res.err), nil
		}
		data, err := json.Marshal(res.out)
		if err != nil {
			return toolError(errors.New("result could not be encoded")), nil
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(data)}},
			StructuredContent: json.RawMessage(data),
		}, nil
	}
}

// toolError reports a failed call to the agent as a tool result, not a
// protocol error, so the agent can read it and correct the call.
func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

// decodeArgs reads tool arguments into dst, refusing unknown fields and
// trailing content. The error names the problem, never the offending value.
func decodeArgs(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return fmt.Errorf("invalid arguments: field %q has the wrong type", typeErr.Field)
		}
		if msg := err.Error(); len(msg) > 0 && bytes.HasPrefix([]byte(msg), []byte("json: unknown field")) {
			return fmt.Errorf("invalid arguments: %s", msg[len("json: "):])
		}
		return errors.New("invalid arguments: not a JSON object matching the input schema")
	}
	if dec.More() {
		return errors.New("invalid arguments: trailing content")
	}
	return nil
}
