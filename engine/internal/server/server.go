package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/segmentio/encoding/json"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"sync"

	"github.com/attest-framework/attest/engine/pkg/types"
)

// Handler is the function signature for JSON-RPC method handlers.
type Handler func(session *Session, params json.RawMessage) (any, *types.RPCError)

// defaultMaxConcurrent is the default value for maxConcurrent (sequential behavior).
const defaultMaxConcurrent = 1

// Server reads NDJSON requests from an io.Reader and writes NDJSON responses to an io.Writer.
type Server struct {
	reader        *bufio.Reader
	writer        *bufio.Writer
	mu            sync.Mutex // protects writer
	session       *Session
	handlers      map[string]Handler
	logger        *slog.Logger
	maxConcurrent int
	semaphore     chan struct{}
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
	return &Server{
		reader:        bufio.NewReader(in),
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
	lines := make(chan inputLine)
	scanErr := make(chan error, 1)

	go func() {
		for {
			l, err := readLine(s.reader)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					scanErr <- err
				}
				close(lines)
				return
			}
			lines <- l
		}
	}()

	// dispatchOne acquires a semaphore slot, dispatches the request, writes the
	// response, then releases the slot. When maxConcurrent == 1 it is called
	// synchronously so behavior is identical to the previous sequential loop.
	dispatchOne := func(in inputLine) {
		if in.oversize {
			s.writeNotification(oversizeResponse(in.prefix))
			return
		}
		line := in.data
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

// MaxLineBytes bounds one request line. A longer line is answered with an
// error and discarded; the engine keeps serving.
const MaxLineBytes = 10 * 1024 * 1024

// prefixBytes is how much of an over-long line is kept to find its id.
const prefixBytes = 4096

type inputLine struct {
	data     []byte
	prefix   []byte
	oversize bool
}

var idPrefix = regexp.MustCompile(`"id"\s*:\s*(-?[0-9]{1,18})\b`)

// readLine reads one newline-terminated line. A line over MaxLineBytes is
// consumed to its end and reported as oversize with its first bytes.
func readLine(r *bufio.Reader) (inputLine, error) {
	var buf []byte
	oversize := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !oversize {
			buf = append(buf, chunk...)
			if len(buf) > MaxLineBytes {
				oversize = true
				buf = buf[:prefixBytes]
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !(errors.Is(err, io.EOF) && (len(buf) > 0 || oversize)) {
			return inputLine{}, err
		}
		if oversize {
			return inputLine{oversize: true, prefix: buf}, nil
		}
		return inputLine{data: bytes.TrimRight(buf, "\r\n")}, nil
	}
}

// oversizeResponse is the JSON-RPC error for an over-long line. It carries the
// request's id when it appears in the first bytes, otherwise null (JSON-RPC
// 2.0, id unknown).
func oversizeResponse(prefix []byte) map[string]any {
	var id any
	if m := idPrefix.FindSubmatch(prefix); m != nil {
		if n, err := strconv.ParseInt(string(m[1]), 10, 64); err == nil {
			id = n
		}
	}
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32600,
			"message": "request too large",
			"data": map[string]any{
				"error_type": "REQUEST_TOO_LARGE",
				"retryable":  false,
				"detail":     fmt.Sprintf("a request line is limited to %d bytes; send fewer or smaller assertions per batch", MaxLineBytes),
			},
		},
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
