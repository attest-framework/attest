package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/attest-framework/attest/engine/internal/llm"
	"github.com/attest-framework/attest/engine/internal/llm/providers/acptest"
)

// TestMain lets this test binary act as the stub ACP agent: with
// a scenario flag (acptest.Command), it serves the scenario on stdio instead
// of running tests.
func TestMain(m *testing.M) {
	if scenario, pidFile := acptest.ScenarioFromArgs(os.Args[1:]); scenario != "" {
		if err := acptest.Serve(os.Stdin, os.Stdout, scenario, pidFile); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// testACPTimeout bounds stub calls that are expected to finish.
const testACPTimeout = 30 * time.Second

// clearCI unsets every CI marker for the test, restoring them afterwards.
func clearCI(t *testing.T) {
	t.Helper()
	for _, k := range CIEnvVars {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func newStubACP(t *testing.T, scenario string) *ACP {
	t.Helper()
	clearCI(t)
	p, err := NewACP(acptest.Command(scenario, ""), "", testACPTimeout)
	if err != nil {
		t.Fatalf("NewACP: %v", err)
	}
	return p
}

func acpJudgeRequest() *llm.CompletionRequest {
	return &llm.CompletionRequest{
		SystemPrompt: `Return JSON {"score": <0..1>, "explanation": "..."}.`,
		Messages:     []llm.Message{{Role: "user", Content: "<agent_output>hello</agent_output>"}},
		MaxTokens:    256,
	}
}

func TestACP_Complete_StubAgent_ReturnsOnlySessionMessageText(t *testing.T) {
	p := newStubACP(t, acptest.Judge)
	for _, k := range []string{"ATTEST_JUDGE_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		t.Setenv(k, "secret-key-that-must-not-reach-the-agent")
	}

	resp, err := p.Complete(context.Background(), acpJudgeRequest())

	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != `{"score": 0.95, "explanation": "helpful answer"}` {
		t.Errorf("content = %q, want the stub's verdict with no thought or foreign-session text", resp.Content)
	}
	if resp.Model != p.DefaultModel() || !strings.HasPrefix(p.DefaultModel(), "acp:") {
		t.Errorf("model = %q / default %q, want the acp:<command> label", resp.Model, p.DefaultModel())
	}
}

func TestACP_Complete_AgentFailures_ReturnErrors(t *testing.T) {
	cases := map[string]string{
		acptest.PromptError:     "Authentication required",
		acptest.ExitEarly:       "agent exited before responding",
		acptest.Refusal:         `stopReason "refusal"`,
		acptest.VersionMismatch: "protocol version 2",
	}
	for scenario, want := range cases {
		t.Run(scenario, func(t *testing.T) {
			p := newStubACP(t, scenario)

			_, err := p.Complete(context.Background(), acpJudgeRequest())

			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want it to contain %q", err, want)
			}
		})
	}
}

func TestNewACP_CIMarkerSet_RefusesWithExplanation(t *testing.T) {
	cases := map[string][2]string{
		"CI=true":          {"CI", "true"},
		"empty CI":         {"CI", ""},
		"GITHUB_ACTIONS":   {"GITHUB_ACTIONS", "true"},
		"JENKINS_URL only": {"JENKINS_URL", "http://jenkins"},
	}
	for name, kv := range cases {
		t.Run(name, func(t *testing.T) {
			clearCI(t)
			t.Setenv(kv[0], kv[1])

			_, err := NewACP(os.Args[0], "", testACPTimeout)

			if err == nil {
				t.Fatalf("NewACP succeeded with %s set", kv[0])
			}
			for _, want := range []string{"CI is set", kv[0] + " is defined", "local use only", "anthropic"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestNewACP_InvalidCommand_Errors(t *testing.T) {
	clearCI(t)
	cases := map[string]string{
		"":                             "requires ATTEST_JUDGE_ACP_COMMAND",
		"   ":                          "requires ATTEST_JUDGE_ACP_COMMAND",
		"attest-no-such-acp-agent --x": "not an executable on PATH",
	}
	for command, want := range cases {
		_, err := NewACP(command, "", testACPTimeout)

		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("NewACP(%q) err = %v, want %q", command, err, want)
		}
	}
}

func TestNewACP_ModelLabel_RecordsConfiguredModel(t *testing.T) {
	clearCI(t)

	p, err := NewACP(os.Args[0]+" --flag", "claude-subscription", testACPTimeout)

	if err != nil {
		t.Fatal(err)
	}
	if p.DefaultModel() != "claude-subscription" || len(p.argv) != 2 {
		t.Errorf("model %q argv %v", p.DefaultModel(), p.argv)
	}
}

func TestNewACP_NonPositiveTimeout_Errors(t *testing.T) {
	clearCI(t)

	_, err := NewACP(os.Args[0], "", 0)

	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want a timeout error", err)
	}
}

func TestNewACP_MissingExecutable_ErrorOmitsArgumentsAndDirectory(t *testing.T) {
	clearCI(t)

	_, err := NewACP("/private-dir/attest-no-such-agent --token=SECRET-ARG", "", testACPTimeout)

	if err == nil || !strings.Contains(err.Error(), `"attest-no-such-agent"`) {
		t.Fatalf("err = %v, want the executable's base name", err)
	}
	if strings.Contains(err.Error(), "SECRET-ARG") || strings.Contains(err.Error(), "private-dir") {
		t.Errorf("error exposes the command line: %q", err)
	}
}

func TestAllowlistedAgentEnv_KeepsOnlyAllowlistedNames(t *testing.T) {
	env := []string{
		"PATH=/bin", "LC_ALL=C", "CLAUDE_CODE_OAUTH_TOKEN=oauth", "HTTPS_PROXY=http://proxy",
		"GITHUB_TOKEN=gh", "AWS_SECRET_ACCESS_KEY=aws", "ATTEST_JUDGE_MODEL=m", "FOO=bar", "Path=/win",
	}

	got := allowlistedAgentEnv(env, false)

	want := []string{"PATH=/bin", "LC_ALL=C", "CLAUDE_CODE_OAUTH_TOKEN=oauth", "HTTPS_PROXY=http://proxy"}
	if !slices.Equal(got, want) {
		t.Errorf("allowlist = %v, want %v", got, want)
	}
}

func TestAllowlistedAgentEnv_FoldCase_MatchesWindowsNamesInAnyCase(t *testing.T) {
	env := []string{"Path=C:\\bin", "userprofile=C:\\Users\\dev", "SystemRoot=C:\\Windows", "lc_all=C", "github_token=gh", "Foo=bar"}

	windows := allowlistedAgentEnv(env, true)
	unix := allowlistedAgentEnv(env, false)

	if want := []string{"Path=C:\\bin", "userprofile=C:\\Users\\dev", "SystemRoot=C:\\Windows", "lc_all=C"}; !slices.Equal(windows, want) {
		t.Errorf("case-insensitive allowlist = %v, want %v", windows, want)
	}
	if len(unix) != 0 {
		t.Errorf("case-sensitive allowlist = %v, want nothing", unix)
	}
}

func TestACP_Complete_DumpEnv_ChildSeesOnlyAllowlistedVariables(t *testing.T) {
	p := newStubACP(t, acptest.DumpEnv)
	for k, v := range map[string]string{
		"GITHUB_TOKEN": "gh-secret", "AWS_SECRET_ACCESS_KEY": "aws-secret", "FOO": "bar",
		"ATTEST_JUDGE_API_KEY": "judge-secret", "OPENAI_API_KEY": "openai-secret",
		"CLAUDE_CODE_OAUTH_TOKEN": "oauth", "CLAUDE_CONFIG_DIR": "/cfg", "CODEX_HOME": "/codex",
		"HTTPS_PROXY": "http://proxy:3128", "LC_ALL": "C",
	} {
		t.Setenv(k, v)
	}

	resp, err := p.Complete(context.Background(), acpJudgeRequest())

	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var dump []string
	if err := json.Unmarshal([]byte(resp.Content), &dump); err != nil {
		t.Fatalf("env dump %q: %v", resp.Content, err)
	}
	seen := map[string]string{}
	for _, kv := range dump {
		k, v, _ := strings.Cut(kv, "=")
		seen[k] = v
	}
	for _, k := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "FOO", "ATTEST_JUDGE_API_KEY", "OPENAI_API_KEY"} {
		if _, ok := seen[k]; ok {
			t.Errorf("%s reached the agent", k)
		}
	}
	for k, want := range map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": "oauth", "CLAUDE_CONFIG_DIR": "/cfg", "CODEX_HOME": "/codex",
		"HTTPS_PROXY": "http://proxy:3128", "LC_ALL": "C",
	} {
		if seen[k] != want {
			t.Errorf("%s = %q in the agent, want %q", k, seen[k], want)
		}
	}
}

func TestACPConn_UpdateBeforeSession_Ignored(t *testing.T) {
	c := newACPConn(io.Discard, strings.NewReader(""))
	update := acpMessage{Method: "session/update", Params: json.RawMessage(
		`{"sessionId":"","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"{\"score\": 1}"}}}`)}

	if err := c.notify(update); err != nil {
		t.Fatal(err)
	}

	if c.text.Len() != 0 {
		t.Errorf("text collected before session/new: %q", c.text.String())
	}
}

func TestACPConn_UnsupportedMethod_ErrorSanitizedAndBounded(t *testing.T) {
	var out bytes.Buffer
	c := newACPConn(&out, strings.NewReader(""))
	method := "\x1b]0;\u202efs/" + strings.Repeat("x", 4*maxErrorDetail)

	if err := c.answer(acpMessage{ID: json.RawMessage(`1`), Method: method}); err != nil {
		t.Fatal(err)
	}

	var resp acpMessage
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || resp.Error == nil {
		t.Fatalf("response %q: %v", out.String(), err)
	}
	if strings.ContainsAny(resp.Error.Message, "\x1b\u202e") || len(resp.Error.Message) > 2*maxErrorDetail {
		t.Errorf("error message unsanitized or unbounded (%d bytes)", len(resp.Error.Message))
	}
}
