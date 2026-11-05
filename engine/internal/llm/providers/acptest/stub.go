// Package acptest provides a scripted Agent Client Protocol agent for tests.
// A test binary runs it from TestMain when ScenarioFromArgs finds a scenario
// flag, so ATTEST_JUDGE_ACP_COMMAND can point at the test binary itself. The
// scenario travels on the command line because the engine starts the agent
// with an allowlisted environment that drops test-only variables.
package acptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ScenarioFlag and PIDFileFlag are the command-line flags that select the
// scenario the stub agent plays and the file Hang writes its child's PID to.
const (
	ScenarioFlag = "--acp-stub"
	PIDFileFlag  = "--acp-stub-pidfile"
)

// Command returns the agent command line that makes the running test binary
// play scenario. pidFile may be empty unless scenario is Hang.
func Command(scenario, pidFile string) string {
	cmd := os.Args[0] + " " + ScenarioFlag + "=" + scenario
	if pidFile != "" {
		cmd += " " + PIDFileFlag + "=" + pidFile
	}
	return cmd
}

// ScenarioFromArgs returns the scenario and PID file named by args, or an
// empty scenario when the binary was not started as the stub agent.
func ScenarioFromArgs(args []string) (scenario, pidFile string) {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, ScenarioFlag+"="); ok {
			scenario = v
		}
		if v, ok := strings.CutPrefix(a, PIDFileFlag+"="); ok {
			pidFile = v
		}
	}
	return scenario, pidFile
}

// Scenarios.
const (
	// Judge answers the prompt with a passing verdict, after checking that
	// the client rejects permission requests, refuses file-system requests,
	// advertises no capabilities and did not pass ATTEST_JUDGE_API_KEY. Any
	// violation turns the verdict into score 0 with the violations listed.
	Judge = "judge"
	// PromptError answers session/prompt with a JSON-RPC error.
	PromptError = "prompt-error"
	// ExitEarly exits when asked for a session.
	ExitEarly = "exit-early"
	// Refusal ends the turn with stopReason "refusal".
	Refusal = "refusal"
	// VersionMismatch answers initialize with protocol version 2.
	VersionMismatch = "version-mismatch"
	// Hang starts a child process that shares the stub's stdout and stderr
	// (as npx does), writes the child's PID to the file named by PIDFileFlag,
	// and never answers session/prompt.
	Hang = "hang"
	// DumpEnv answers the prompt with the stub's own environment as a JSON
	// array of NAME=value strings, so a test can assert what the engine let
	// reach the agent.
	DumpEnv = "dump-env"
	// sleepChild is the child Hang starts.
	sleepChild = "sleep-child"
)

// SessionID is the session the stub creates.
const SessionID = "stub-session"

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type stub struct {
	in         *bufio.Scanner
	out        io.Writer
	scenario   string
	pidFile    string
	violations []string
}

// Serve plays scenario over r/w until the client closes r. pidFile is where
// Hang records its child's PID.
func Serve(r io.Reader, w io.Writer, scenario, pidFile string) error {
	if scenario == sleepChild {
		time.Sleep(time.Minute)
		return nil
	}
	if scenario == Judge {
		// Wrappers print banners; the client must skip non-JSON lines.
		if _, err := io.WriteString(w, "npx: installed 1 package\n"); err != nil {
			return err
		}
	}
	s := &stub{in: bufio.NewScanner(r), out: w, scenario: scenario, pidFile: pidFile}
	s.in.Buffer(make([]byte, 64<<10), 8<<20)
	for {
		msg, ok, err := s.read()
		if err != nil || !ok {
			return err
		}
		done, err := s.handle(msg)
		if err != nil || done {
			return err
		}
	}
}

func (s *stub) read() (message, bool, error) {
	if !s.in.Scan() {
		return message{}, false, s.in.Err()
	}
	var m message
	if err := json.Unmarshal(s.in.Bytes(), &m); err != nil {
		return message{}, false, fmt.Errorf("stub: client wrote invalid JSON: %w", err)
	}
	return m, true, nil
}

func (s *stub) send(m map[string]any) error {
	m["jsonrpc"] = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.out.Write(append(b, '\n'))
	return err
}

func (s *stub) respond(id json.RawMessage, result any) error {
	return s.send(map[string]any{"id": id, "result": result})
}

func (s *stub) handle(m message) (bool, error) {
	switch m.Method {
	case "initialize":
		s.checkInitialize(m.Params)
		version := 1
		if s.scenario == VersionMismatch {
			version = 2
		}
		return false, s.respond(m.ID, map[string]any{
			"protocolVersion": version, "agentCapabilities": map[string]any{}, "authMethods": []any{},
		})
	case "session/new":
		if s.scenario == ExitEarly {
			return true, nil
		}
		s.checkSessionNew(m.Params)
		return false, s.respond(m.ID, map[string]any{"sessionId": SessionID})
	case "session/prompt":
		return false, s.prompt(m)
	default:
		return false, s.send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "unknown " + m.Method}})
	}
}

func (s *stub) checkInitialize(raw json.RawMessage) {
	var p struct {
		ProtocolVersion    int `json:"protocolVersion"`
		ClientCapabilities struct {
			FS struct {
				Read  bool `json:"readTextFile"`
				Write bool `json:"writeTextFile"`
			} `json:"fs"`
			Terminal bool `json:"terminal"`
		} `json:"clientCapabilities"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		s.violate("initialize params: " + err.Error())
		return
	}
	if p.ProtocolVersion != 1 {
		s.violate(fmt.Sprintf("protocolVersion %d", p.ProtocolVersion))
	}
	if p.ClientCapabilities.FS.Read || p.ClientCapabilities.FS.Write || p.ClientCapabilities.Terminal {
		s.violate("client advertised fs or terminal capabilities")
	}
}

func (s *stub) checkSessionNew(raw json.RawMessage) {
	var p struct {
		Cwd string `json:"cwd"`
	}
	_ = json.Unmarshal(raw, &p)
	if !filepath.IsAbs(p.Cwd) {
		s.violate("cwd is not absolute: " + p.Cwd)
		return
	}
	if entries, err := os.ReadDir(p.Cwd); err != nil || len(entries) != 0 {
		s.violate("cwd is not an empty directory")
	}
}

func (s *stub) prompt(m message) error {
	switch s.scenario {
	case Hang:
		if err := startSleepingChild(s.pidFile); err != nil {
			return err
		}
		select {}
	case PromptError:
		return s.send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32000, "message": "Authentication required"}})
	case Refusal:
		return s.respond(m.ID, map[string]any{"stopReason": "refusal"})
	}
	var p struct {
		Prompt []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	_ = json.Unmarshal(m.Params, &p)
	if len(p.Prompt) != 1 || !strings.Contains(p.Prompt[0].Text, "score") {
		s.violate("prompt does not carry the rubric and output as one text block")
	}
	for _, k := range []string{"ATTEST_JUDGE_API_KEY", "ATTEST_JUDGE_MODEL", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		if _, ok := os.LookupEnv(k); ok {
			s.violate(k + " reached the agent")
		}
	}
	if err := s.askPermission(); err != nil {
		return err
	}
	if err := s.askFileRead(); err != nil {
		return err
	}
	verdict := `{"score": 0.95, "explanation": "helpful answer"}`
	if s.scenario == DumpEnv {
		b, err := json.Marshal(os.Environ())
		if err != nil {
			return err
		}
		verdict = string(b)
	} else if len(s.violations) > 0 {
		b, _ := json.Marshal(strings.Join(s.violations, "; "))
		verdict = `{"score": 0.0, "explanation": ` + string(b) + `}`
	}
	half := len(verdict) / 2
	updates := []map[string]any{
		{"sessionId": SessionID, "update": map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": "call-1", "title": "Read file", "kind": "read", "status": "pending",
			"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "tool output {"}}},
		}},
		{"sessionId": SessionID, "update": map[string]any{
			"sessionUpdate": "plan", "entries": []map[string]any{{"content": "judge", "priority": "high", "status": "pending"}},
		}},
		chunk(SessionID, "agent_thought_chunk", "thinking that must not reach the verdict {"),
		chunk("other-session", "agent_message_chunk", "text from another session }"),
		chunk(SessionID, "agent_message_chunk", verdict[:half]),
		chunk(SessionID, "agent_message_chunk", verdict[half:]),
	}
	for _, u := range updates {
		if err := s.send(map[string]any{"method": "session/update", "params": u}); err != nil {
			return err
		}
	}
	return s.respond(m.ID, map[string]any{"stopReason": "end_turn"})
}

func chunk(session, kind, text string) map[string]any {
	return map[string]any{"sessionId": session, "update": map[string]any{
		"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text},
	}}
}

// askPermission requests to run a tool; the client must pick the reject
// option.
func (s *stub) askPermission() error {
	if err := s.send(map[string]any{"id": "perm-1", "method": "session/request_permission", "params": map[string]any{
		"sessionId": SessionID,
		"toolCall":  map[string]any{"toolCallId": "call-1", "title": "rm -rf ~"},
		"options": []map[string]any{
			{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
			{"optionId": "deny", "name": "Reject", "kind": "reject_once"},
		},
	}}); err != nil {
		return err
	}
	resp, ok, err := s.read()
	if err != nil || !ok {
		return fmt.Errorf("stub: no permission response: %v", err)
	}
	var r struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	_ = json.Unmarshal(resp.Result, &r)
	if string(resp.ID) != `"perm-1"` || r.Outcome.Outcome != "selected" || r.Outcome.OptionID != "deny" {
		s.violate("permission request was not rejected: " + string(resp.Result))
	}
	return nil
}

// askFileRead requests a file; the client must answer method-not-found.
func (s *stub) askFileRead() error {
	if err := s.send(map[string]any{"id": 7, "method": "fs/read_text_file", "params": map[string]any{
		"sessionId": SessionID, "path": "/etc/passwd",
	}}); err != nil {
		return err
	}
	resp, ok, err := s.read()
	if err != nil || !ok {
		return fmt.Errorf("stub: no fs response: %v", err)
	}
	if string(resp.ID) != "7" || resp.Error == nil || resp.Error.Code != -32601 || resp.Result != nil {
		s.violate("fs/read_text_file was not refused")
	}
	return nil
}

func startSleepingChild(pidFile string) error {
	child := exec.Command(os.Args[0], ScenarioFlag+"="+sleepChild)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		return err
	}
	return os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
}

func (s *stub) violate(v string) { s.violations = append(s.violations, v) }
