package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// Handler is the function signature for JSON-RPC method handlers.
type Handler func(session *Session, params json.RawMessage) (any, *types.RPCError)

// defaultMaxConcurrent is the default value for maxConcurrent (sequential behavior).
const defaultMaxConcurrent = 1

// Server reads NDJSON requests from an io.Reader and writes NDJSON responses to an io.Writer.
type Server struct {
	reader         *bufio.Scanner
	writer         *bufio.Writer
	mu             sync.Mutex // protects writer
	session        *Session
	handlers       map[string]Handler
	logger         *slog.Logger
	maxConcurrent  int
	semaphore      chan struct{}
}

// New creates a new Server reading from in and writing to out.
// maxConcurrent controls request parallelism (0 or 1 → sequential; >1 → concurrent).
// Use NewDefault for the standard sequential server.
func New(in io.Reader, out io.Writer, logger *slog.Logger) *Server {
	return NewWithConcurrency(in, out, logger, defaultMaxConcurrent)
}

// NewWithConcurrency creates a Server with a configurable concurrency limit.
// When maxConcurrent <= 1, requests are processed sequentially (default behavior).
// When maxConcurrent > 1, up to maxConcurrent requests are dispatched concurrently,
// each holding a semaphore slot for the duration of handler execution.
func NewWithConcurrency(in io.Reader, out io.Writer, logger *slog.Logger, maxConcurrent int) *Server {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	scanner := bufio.NewScanner(in)
	// 10 MB buffer for large traces.
	const maxScanBuf = 10 * 1024 * 1024
	scanner.Buffer(make([]byte, maxScanBuf), maxScanBuf)

	return &Server{
		reader:        scanner,
		writer:        bufio.NewWriter(out),
		session:       NewSession(),
		handlers:      make(map[string]Handler),
		logger:        logger,
		maxConcurrent: maxConcurrent,
		semaphore:     make(chan struct{}, maxConcurrent),
	}
}

// RegisterHandler registers a handler for the given JSON-RPC method name.
func (s *Server) RegisterHandler(method string, h Handler) {
	s.handlers[method] = h
}

// Run reads NDJSON lines from the reader, dispatches to handlers, and writes responses until
// stdin is closed or the context is canceled.
func (s *Server) Run(ctx context.Context) error {
	lines := make(chan []byte)
	scanErr := make(chan error, 1)

	go func() {
		for s.reader.Scan() {
			line := make([]byte, len(s.reader.Bytes()))
			copy(line, s.reader.Bytes())
			lines <- line
		}
		if err := s.reader.Err(); err != nil {
			scanErr <- err
		}
		close(lines)
	}()

	// dispatchOne acquires a semaphore slot, dispatches the request, writes the
	// response, then releases the slot. When maxConcurrent == 1 it is called
	// synchronously so behavior is identical to the previous sequential loop.
	dispatchOne := func(line []byte) {
		s.semaphore <- struct{}{}
		handle := func() {
			defer func() { <-s.semaphore }()
			resp := s.dispatch(line)
			s.writeResponse(resp)
		}
		if s.maxConcurrent > 1 {
			go handle()
		} else {
			handle()
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-scanErr:
			return err
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			dispatchOne(line)
			if s.session.State() == StateShuttingDown {
				return nil
			}
		}
	}
}

// dispatch parses a raw JSON line into a Request and routes it to the appropriate handler.
func (s *Server) dispatch(line []byte) *types.Response {
	var req types.Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.logger.Error("parse error", "err", err)
		return types.NewErrorResponse(0, &types.RPCError{
			Code:    -32700,
			Message: "parse error",
			Data: &types.ErrorData{
				ErrorType: "PARSE_ERROR",
				Retryable: false,
				Detail:    err.Error(),
			},
		})
	}

	if req.JSONRPC != "2.0" || req.Method == "" {
		s.logger.Error("invalid request", "req", req)
		return types.NewErrorResponse(req.ID, &types.RPCError{
			Code:    -32600,
			Message: "invalid request",
			Data: &types.ErrorData{
				ErrorType: "INVALID_REQUEST",
				Retryable: false,
				Detail:    "jsonrpc must be \"2.0\" and method must be non-empty",
			},
		})
	}

	h, ok := s.handlers[req.Method]
	if !ok {
		s.logger.Warn("method not found", "method", req.Method)
		return types.NewErrorResponse(req.ID, &types.RPCError{
			Code:    -32601,
			Message: "method not found",
			Data: &types.ErrorData{
				ErrorType: "METHOD_NOT_FOUND",
				Retryable: false,
				Detail:    "unknown method: " + req.Method,
			},
		})
	}

	result, rpcErr := h(s.session, req.Params)
	if rpcErr != nil {
		return types.NewErrorResponse(req.ID, rpcErr)
	}

	resp, err := types.NewSuccessResponse(req.ID, result)
	if err != nil {
		s.logger.Error("failed to marshal result", "method", req.Method, "err", err)
		return types.NewErrorResponse(req.ID, types.NewRPCError(
			types.ErrEngineError,
			"failed to marshal result",
			types.ErrTypeEngineError,
			false,
			err.Error(),
		))
	}
	return resp
}

// writeResponse serializes a Response as compact JSON followed by a newline.
func (s *Server) writeResponse(resp *types.Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("failed to marshal response", "err", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.writer.Write(data)
	_ = s.writer.WriteByte('\n')
	_ = s.writer.Flush()
}

// writeNotification serializes an arbitrary value as compact JSON followed by a newline,
// using the same mutex as writeResponse to prevent races with concurrent response writes.
func (s *Server) writeNotification(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		s.logger.Error("failed to marshal notification", "err", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.writer.Write(data)
	_ = s.writer.WriteByte('\n')
	_ = s.writer.Flush()
}
