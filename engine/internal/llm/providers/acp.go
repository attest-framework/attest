package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/attest-framework/attest/engine/internal/buildinfo"
	"github.com/attest-framework/attest/engine/internal/llm"
	"github.com/attest-framework/attest/engine/internal/redact"
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
// file-system or terminal capability, refuses those requests and rejects
// every permission request, which blocks actions routed through the client.
// It is not a sandbox: tools the agent's own configuration allows without
// asking still run as the user.
type ACP struct {
	argv    []string
	model   string
	timeout time.Duration
}

// NewACP returns an ACP provider for command, split on whitespace (no shell
// quoting or expansion). model is a label recorded on results; ACP has no
// stable model selection, so it is not sent to the agent. An empty model
// records "acp:<command name>". timeout bounds every Complete on its own,
// because some callers (simulated users) pass a context with no deadline.
// ACP judges use a personal subscription and are refused in CI. Errors name
// only the executable's base name: arguments may be sensitive.
func NewACP(command, model string, timeout time.Duration) (*ACP, error) {
	if name, ok := ciMarker(); ok {
		return nil, fmt.Errorf(
			"ATTEST_JUDGE_PROVIDER=acp is refused because CI is set (%s is defined): an ACP judge runs on a developer's "+
				"personal Claude Code or Codex subscription and is for local use only; in CI use "+
				"ATTEST_JUDGE_PROVIDER=openai, anthropic or gemini with an API key", name)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("acp provider: timeout must be > 0, got %v", timeout)
	}
	argv := strings.Fields(command)
	if len(argv) == 0 {
		return nil, errors.New("ATTEST_JUDGE_PROVIDER=acp requires ATTEST_JUDGE_ACP_COMMAND, the command that starts the ACP agent")
	}
	name := filepath.Base(argv[0])
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("ATTEST_JUDGE_ACP_COMMAND: %q is not an executable on PATH", name)
	}
	// The agent runs in a temporary directory, so a relative path must be
	// resolved against the engine's working directory now.
	if argv[0], err = filepath.Abs(path); err != nil {
		return nil, fmt.Errorf("ATTEST_JUDGE_ACP_COMMAND: cannot resolve the path of %q", name)
	}
	if model == "" {
		model = "acp:" + name
	}
	return &ACP{argv: argv, model: model, timeout: timeout}, nil
}

// CIEnvVars are the variables whose presence, with any value including
// empty, marks a CI environment. CI is the convention most providers set;
// the rest catch wrappers that drop CI but keep the vendor variable.
var CIEnvVars = []string{
	"CI", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI", "JENKINS_URL", "TF_BUILD", "TEAMCITY_VERSION",
}

func ciMarker() (string, bool) {
	for _, name := range CIEnvVars {
		if _, ok := os.LookupEnv(name); ok {
			return name, true
		}
	}
	return "", false
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

	// The earlier of the caller's deadline and the provider's own timeout.
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.argv[0], p.argv[1:]...)
	cmd.Dir = dir
	cmd.Env = agentEnv()
	// Wrappers such as npx keep the pipes open in grandchildren: the agent
	// gets its own process group, which cancellation kills as a whole, and
	// Wait stops waiting for the pipes after acpShutdownGrace.
	startInProcessGroup(cmd)
	cmd.WaitDelay = acpShutdownGrace
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
	// A timed-out judgment must not block on the agent: close its stdout so
	// the read loop returns even if a descendant still holds the pipe.
	stopReading := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stopReading()

	text, err := runJudgeTurn(newACPConn(stdin, stdout), dir, acpPromptText(req))
	if err != nil {
		if tail := strings.TrimSpace(stripControl(stderr.String())); tail != "" {
			return nil, fmt.Errorf("%w (agent stderr: %s)", err, tail)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("acp: %w", err)
	}
	return &llm.CompletionResponse{
		Content:    stripTerminalControl(text),
		Model:      p.model,
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// agentEnvNames are the only variables, besides the LC_* locale family (minus
// names the redact rule treats as secrets, since LC_* is a wildcard that ssh
// setups also use to forward arbitrary values), the ACP agent inherits: process basics, the Windows system variables a child
// cannot start without, proxy and CA settings for corporate networks, and the
// three variables that carry the agent's own session login. The allowlist
// takes precedence over the redact suffix rule, so CLAUDE_CODE_OAUTH_TOKEN
// passes although its name ends in TOKEN. Everything else, including every
// provider key and ATTEST_JUDGE_* switch, is dropped.
var agentEnvNames = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TERM", "LANG",
	"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR", "CODEX_HOME",
	"USERPROFILE", "APPDATA", "LOCALAPPDATA", "SYSTEMROOT", "COMSPEC", "PATHEXT",
	"TEMP", "TMP", "HOMEDRIVE", "HOMEPATH",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy",
	"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS",
}

// agentEnv is the engine's environment reduced to the allowlist.
func agentEnv() []string {
	return allowlistedAgentEnv(os.Environ(), runtime.GOOS == "windows")
}

// allowlistedAgentEnv keeps the entries of env whose names are allowlisted.
// Windows variable names are case-insensitive, so there both the name and the
// allowlist are compared in upper case; elsewhere names match exactly.
func allowlistedAgentEnv(env []string, foldCase bool) []string {
	out := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if foldCase {
			name = strings.ToUpper(name)
		}
		if slices.Contains(agentEnvNames, name) || isLocaleVar(name) {
			out = append(out, kv)
		}
	}
	return out
}

// isLocaleVar reports whether name belongs to the LC_* wildcard of the
// allowlist. Explicit names override the redact suffix rule; the wildcard does
// not, so LC_DEPLOY_TOKEN stays out.
func isLocaleVar(name string) bool {
	return strings.HasPrefix(name, "LC_") && !redact.IsSecretEnvName(name)
}

// stopAgent closes the agent's stdin, which ends a well-behaved agent, and
// kills its process group if it has not exited within acpShutdownGrace. The
// group is killed only while its leader is alive: after the leader is
// reaped its PID may be reused.
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
		"clientInfo": map[string]string{"name": "attest-engine", "version": buildinfo.Version},
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
		line := bytes.TrimSpace(c.r.Bytes())
		if len(line) == 0 || line[0] != '{' {
			// Not a JSON-RPC message (a wrapper's banner, for example).
			continue
		}
		var msg acpMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			return fmt.Errorf("acp %s: agent wrote a line that is not JSON-RPC: %w", method, err)
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			if err := c.answer(msg); err != nil {
				return fmt.Errorf("acp %s: answer %s: %w", method, truncate(stripControl(msg.Method), maxErrorDetail), err)
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
	// content is an object for message chunks but an array for tool calls,
	// so it is decoded only once the update kind is known.
	var p struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string          `json:"sessionUpdate"`
			Content       json.RawMessage `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return fmt.Errorf("decode session/update: %w", err)
	}
	// Updates before session/new returns belong to no session of ours.
	if c.sessionID == "" || p.SessionID != c.sessionID || p.Update.SessionUpdate != "agent_message_chunk" {
		return nil
	}
	var content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(p.Update.Content, &content); err != nil {
		return fmt.Errorf("decode agent_message_chunk: %w", err)
	}
	if content.Type != "text" {
		return nil
	}
	if c.text.Len()+len(content.Text) > maxResponseBytes {
		return fmt.Errorf("agent message exceeds %d bytes", maxResponseBytes)
	}
	c.text.WriteString(content.Text)
	return nil
}

// answer responds to an agent-to-client request. Permission requests are
// rejected; every other method (file system, terminal) is unsupported.
func (c *acpConn) answer(msg acpMessage) error {
	if msg.Method != "session/request_permission" {
		return c.write(acpMessage{ID: msg.ID, Error: &acpError{
			Code:    -32601,
			Message: "the attest judge client does not support " + truncate(stripControl(msg.Method), maxErrorDetail),
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
