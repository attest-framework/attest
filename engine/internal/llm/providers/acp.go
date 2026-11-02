package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/attest-ai/attest/engine/internal/llm"
)

// acpProtocolVersion is the Agent Client Protocol major version this client
// speaks.
const acpProtocolVersion = 1

// acpShutdownGrace is how long an agent gets to exit after its stdin closes
// before it is killed.
const acpShutdownGrace = 2 * time.Second

// acpStderrTail caps how much of the agent's stderr is kept for errors.
const acpStderrTail = 512

// ACP implements llm.Provider by running a judge prompt through a local
// Agent Client Protocol agent (for example a Claude Code or Codex ACP
// adapter) on the developer's own subscription. Each Complete spawns the
// agent in an empty temporary directory, opens one session, sends one
// prompt and collects the agent's message text. The client advertises no
// file-system or terminal capability and rejects every permission request,
// so the agent cannot act on the untrusted agent output inside the prompt.
type ACP struct {
	argv  []string
	model string
}

// NewACP returns an ACP provider for command, split on whitespace (no shell
// quoting or expansion). model is a label recorded on results; ACP has no
// stable model selection, so it is not sent to the agent. An empty model
// records "acp:<command name>". ACP judges use a personal subscription and
// are refused when the CI environment variable is set.
func NewACP(command, model string) (*ACP, error) {
	if ci := os.Getenv("CI"); ci != "" {
		return nil, fmt.Errorf(
			"ATTEST_JUDGE_PROVIDER=acp is refused because CI is set (CI=%q): an ACP judge runs on a developer's "+
				"personal Claude Code or Codex subscription and is for local use only; in CI use "+
				"ATTEST_JUDGE_PROVIDER=openai, anthropic or gemini with an API key", ci)
	}
	argv := strings.Fields(command)
	if len(argv) == 0 {
		return nil, errors.New("ATTEST_JUDGE_PROVIDER=acp requires ATTEST_JUDGE_ACP_COMMAND, the command that starts the ACP agent")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("ATTEST_JUDGE_ACP_COMMAND: %q is not an executable on PATH: %w", argv[0], err)
	}
	argv[0] = path
	if model == "" {
		model = "acp:" + filepath.Base(path)
	}
	return &ACP{argv: argv, model: model}, nil
}

// Name returns "acp".
func (p *ACP) Name() string { return "acp" }

// DefaultModel returns the model label.
func (p *ACP) DefaultModel() string { return p.model }

// Complete runs one prompt turn on a freshly spawned agent.
func (p *ACP) Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	dir, err := os.MkdirTemp("", "attest-acp-judge-")
	if err != nil {
		return nil, fmt.Errorf("acp: create working directory: %w", err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.argv[0], p.argv[1:]...)
	cmd.Dir = dir
	cmd.Env = agentEnv()
	stderr := &tailBuffer{max: acpStderrTail}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("acp: start agent: %w", err)
	}
	defer stopAgent(cmd, stdin, cancel)

	text, err := runJudgeTurn(newACPConn(stdin, stdout), dir, acpPromptText(req))
	if err != nil {
		if tail := strings.TrimSpace(stripControl(stderr.String())); tail != "" {
			return nil, fmt.Errorf("%w (agent stderr: %s)", err, tail)
		}
		return nil, err
	}
	return &llm.CompletionResponse{
		Content:    text,
		Model:      p.model,
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// agentEnv is the engine's environment minus ATTEST_JUDGE_API_KEY, which
// belongs to API-key providers and is never handed to a local agent.
func agentEnv() []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "ATTEST_JUDGE_API_KEY=") {
			out = append(out, kv)
		}
	}
	return out
}

// stopAgent closes the agent's stdin, which ends a well-behaved agent, and
// kills it if it has not exited within acpShutdownGrace.
func stopAgent(cmd *exec.Cmd, stdin io.Closer, cancel context.CancelFunc) {
	_ = stdin.Close()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(acpShutdownGrace):
		cancel()
		<-done
	}
}

// acpPromptText flattens the request into one text block: ACP has no
// system-prompt slot, so the rubric's system prompt leads the prompt.
func acpPromptText(req *llm.CompletionRequest) string {
	parts := make([]string, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		parts = append(parts, req.SystemPrompt)
	}
	for _, m := range req.Messages {
		parts = append(parts, m.Content)
	}
	return strings.Join(parts, "\n\n")
}

// runJudgeTurn performs initialize, session/new and one session/prompt,
// returning the agent's concatenated message text.
func runJudgeTurn(c *acpConn, cwd, prompt string) (string, error) {
	var initRes struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := c.call("initialize", map[string]any{
		"protocolVersion": acpProtocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]string{"name": "attest-engine"},
	}, &initRes); err != nil {
		return "", err
	}
	if initRes.ProtocolVersion != acpProtocolVersion {
		return "", fmt.Errorf("acp: agent speaks protocol version %d, attest supports %d", initRes.ProtocolVersion, acpProtocolVersion)
	}

	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}, &session); err != nil {
		return "", err
	}
	if session.SessionID == "" {
		return "", errors.New("acp: session/new returned no sessionId")
	}
	c.sessionID = session.SessionID

	var turn struct {
		StopReason string `json:"stopReason"`
	}
	if err := c.call("session/prompt", map[string]any{
		"sessionId": session.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": prompt}},
	}, &turn); err != nil {
		return "", err
	}
	if turn.StopReason != "end_turn" {
		return "", fmt.Errorf("acp: agent ended the turn with stopReason %q", turn.StopReason)
	}
	if c.text.Len() == 0 {
		return "", errors.New("acp: agent sent no message text")
	}
	return c.text.String(), nil
}

// acpMessage is any JSON-RPC 2.0 message on the wire.
type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// acpConn is a synchronous JSON-RPC client over newline-delimited JSON. A
// call writes one request and reads until its response, answering agent
// requests and collecting session/update text along the way.
type acpConn struct {
	w         io.Writer
	r         *bufio.Scanner
	nextID    int
	sessionID string
	text      strings.Builder
}

func newACPConn(w io.Writer, r io.Reader) *acpConn {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxResponseBytes)
	return &acpConn{w: w, r: sc}
}

func (c *acpConn) write(msg acpMessage) error {
	msg.JSONRPC = "2.0"
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.w.Write(append(b, '\n'))
	return err
}

func (c *acpConn) call(method string, params, result any) error {
	c.nextID++
	id := strconv.Itoa(c.nextID)
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("acp %s: marshal params: %w", method, err)
	}
	if err := c.write(acpMessage{ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		return fmt.Errorf("acp %s: write: %w", method, err)
	}
	for c.r.Scan() {
		var msg acpMessage
		if err := json.Unmarshal(c.r.Bytes(), &msg); err != nil {
			return fmt.Errorf("acp %s: agent wrote a line that is not JSON-RPC: %w", method, err)
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			if err := c.answer(msg); err != nil {
				return fmt.Errorf("acp %s: answer %s: %w", method, msg.Method, err)
			}
		case msg.Method != "":
			if err := c.notify(msg); err != nil {
				return fmt.Errorf("acp %s: %w", method, err)
			}
		case string(msg.ID) == id:
			if msg.Error != nil {
				return fmt.Errorf("acp %s: agent error %d: %s", method, msg.Error.Code, truncate(stripControl(msg.Error.Message), maxErrorDetail))
			}
			if err := json.Unmarshal(msg.Result, result); err != nil {
				return fmt.Errorf("acp %s: decode result: %w", method, err)
			}
			return nil
		}
	}
	if err := c.r.Err(); err != nil {
		return fmt.Errorf("acp %s: read: %w", method, err)
	}
	return fmt.Errorf("acp %s: agent exited before responding", method)
}

// notify collects agent message text from session/update notifications for
// this session; every other notification is ignored.
func (c *acpConn) notify(msg acpMessage) error {
	if msg.Method != "session/update" {
		return nil
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return fmt.Errorf("decode session/update: %w", err)
	}
	if p.SessionID != c.sessionID || p.Update.SessionUpdate != "agent_message_chunk" || p.Update.Content.Type != "text" {
		return nil
	}
	if c.text.Len()+len(p.Update.Content.Text) > maxResponseBytes {
		return fmt.Errorf("agent message exceeds %d bytes", maxResponseBytes)
	}
	c.text.WriteString(p.Update.Content.Text)
	return nil
}

// answer responds to an agent-to-client request. Permission requests are
// rejected; every other method (file system, terminal) is unsupported.
func (c *acpConn) answer(msg acpMessage) error {
	if msg.Method != "session/request_permission" {
		return c.write(acpMessage{ID: msg.ID, Error: &acpError{
			Code: -32601, Message: "the attest judge client does not support " + msg.Method,
		}})
	}
	var p struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return fmt.Errorf("decode permission request: %w", err)
	}
	outcome := map[string]string{"outcome": "cancelled"}
	for _, o := range p.Options {
		if o.Kind == "reject_once" || o.Kind == "reject_always" {
			outcome = map[string]string{"outcome": "selected", "optionId": o.OptionID}
			break
		}
	}
	result, err := json.Marshal(map[string]any{"outcome": outcome})
	if err != nil {
		return err
	}
	return c.write(acpMessage{ID: msg.ID, Result: result})
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
