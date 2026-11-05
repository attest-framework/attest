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
	"sync"
	"time"

	"github.com/attest-framework/attest/engine/pkg/types"
)

// Handler is the function signature for JSON-RPC method handlers.
type Handler func(ctx context.Context, session *Session, params json.RawMessage) (any, *types.RPCError)

// Server reads NDJSON requests from an io.Reader and writes NDJSON responses to an io.Writer.
// Requests are handled one at a time, in arrival order.
type Server struct {
	reader   *bufio.Reader
	writer   *bufio.Writer
	mu       sync.Mutex // protects writer
	session  *Session
	handlers map[string]Handler
	logger   *slog.Logger
}

// New creates a new Server reading from in and writing to out.
func New(in io.Reader, out io.Writer, logger *slog.Logger) *Server {
	return &Server{
		reader:   bufio.NewReader(in),
		writer:   bufio.NewWriter(out),
		session:  NewSession(),
		handlers: make(map[string]Handler),
		logger:   logger,
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

	// inflight tracks the handler goroutine so that shutdown on a signal can
	// wait briefly for it to observe ctx.
	var inflight sync.WaitGroup

	// dispatchOne runs the handler on its own goroutine so the loop below keeps
	// watching ctx while a handler is in flight, and returns when the handler
	// has finished or ctx is cancelled.
	dispatchOne := func(in inputLine) {
		if in.oversize {
			s.writeNotification(oversizeResponse(in.prefix))
			return
		}
		done := make(chan struct{})
		inflight.Add(1)
		go func() {
			defer inflight.Done()
			defer close(done)
			if resp := s.dispatch(ctx, in.data); resp != nil {
				s.writeResponse(resp)
			}
		}()
		select {
		case <-done:
		case <-ctx.Done():
		}
	}

	// drain waits up to shutdownGrace for in-flight handlers, which receive the
	// cancelled ctx, to return.
	drain := func() {
		finished := make(chan struct{})
		go func() { inflight.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(shutdownGrace):
			s.logger.Warn("in-flight requests did not stop before the shutdown grace period")
		}
	}

	for {
		select {
		case <-ctx.Done():
			drain()
			return ctx.Err()
		case err := <-scanErr:
			return err
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			dispatchOne(line)
			if ctx.Err() != nil {
				drain()
				return ctx.Err()
			}
			if s.session.State() == StateShuttingDown {
				return nil
			}
		}
	}
}

// shutdownGrace bounds how long Run waits for in-flight handlers after its
// context is cancelled; the engine must exit within 2 s of SIGTERM.
const shutdownGrace = time.Second

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

var idPrefix = regexp.MustCompile(`"id"\s*:\s*("(?:[^"\\]|\\.)*"|-?[0-9]{1,18})([\s,}]|$)`)

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
		if err != nil && (!errors.Is(err, io.EOF) || (len(buf) == 0 && !oversize)) {
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
	id := json.RawMessage("null")
	if m := idPrefix.FindSubmatch(prefix); m != nil {
		id = json.RawMessage(m[1])
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
// It returns nil when the line is a notification (a request without an id),
// which JSON-RPC 2.0 forbids answering, even with an error.
func (s *Server) dispatch(ctx context.Context, line []byte) *types.Response {
	var req types.Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.logger.Error("parse error", "err", err)
		return types.NewErrorResponse(nil, &types.RPCError{
			Code:    -32700,
			Message: "parse error",
			Data: &types.ErrorData{
				ErrorType: "PARSE_ERROR",
				Retryable: false,
				Detail:    err.Error(),
			},
		})
	}

	notification := len(req.ID) == 0
	// reply wraps a response so that notifications stay unanswered.
	reply := func(resp *types.Response) *types.Response {
		if notification {
			return nil
		}
		return resp
	}

	if !validID(req.ID) {
		s.logger.Error("invalid request id", "id", string(req.ID))
		return types.NewErrorResponse(nil, types.NewRPCError(
			-32600, "invalid request", "INVALID_REQUEST", false,
			"id must be a string, a number or null",
		))
	}

	if req.JSONRPC != "2.0" || req.Method == "" {
		s.logger.Error("invalid request", "jsonrpc", req.JSONRPC, "method", req.Method)
		// An invalid request is never a notification: it is always answered,
		// with the request id when it carried a valid one and null otherwise.
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
		return reply(types.NewErrorResponse(req.ID, &types.RPCError{
			Code:    -32601,
			Message: "method not found",
			Data: &types.ErrorData{
				ErrorType: "METHOD_NOT_FOUND",
				Retryable: false,
				Detail:    "unknown method: " + req.Method,
			},
		}))
	}

	result, rpcErr := h(ctx, s.session, req.Params)
	if rpcErr != nil {
		return reply(types.NewErrorResponse(req.ID, rpcErr))
	}

	resp, err := types.NewSuccessResponse(req.ID, result)
	if err != nil {
		s.logger.Error("failed to marshal result", "method", req.Method, "err", err)
		return reply(types.NewErrorResponse(req.ID, types.NewRPCError(
			types.ErrEngineError,
			"failed to marshal result",
			types.ErrTypeEngineError,
			false,
			err.Error(),
		)))
	}
	return reply(resp)
}

// validID reports whether raw is absent or a JSON string, number or null.
func validID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	switch raw[0] {
	case '"', 'n', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return true
	}
	return false
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
