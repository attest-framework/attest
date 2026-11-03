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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/internal/pathguard"
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

// env is what the tool handlers share. It holds no per-call state.
type env struct {
	root     *pathguard.Root
	pipeline *assertion.Pipeline
}

// NewServer builds the server with its fixed toolset. Logs go to logger.
func NewServer(root *pathguard.Root, version string, logger *slog.Logger) *mcp.Server {
	e := &env{root: root, pipeline: assertion.NewPipeline(assertion.NewRegistry())}
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

func (e *env) handler(def toolDef) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.Params.Arguments
		if len(args) > MaxArgumentBytes {
			return toolError(fmt.Errorf("arguments are larger than %d bytes", MaxArgumentBytes)), nil
		}
		out, err := def.run(e, args)
		if err != nil {
			return toolError(err), nil
		}
		data, err := json.Marshal(out)
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
