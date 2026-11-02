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

	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/internal/llm/providers/acptest"
)

// TestMain lets this test binary act as the stub ACP agent: with
// acptest.StubEnv set, it serves the scenario on stdio instead of running
// tests.
func TestMain(m *testing.M) {
	if scenario := os.Getenv(acptest.StubEnv); scenario != "" {
		if err := acptest.Serve(os.Stdin, os.Stdout, scenario); err != nil {
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
	t.Setenv(acptest.StubEnv, scenario)
	p, err := NewACP(os.Args[0], "", testACPTimeout)
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

func TestScrubAgentEnv_RemovesCredentialsAndRoutingVariables(t *testing.T) {
	env := []string{"PATH=/bin", "HOME=/home/dev", "ATTEST_JUDGE_MODEL=m", "anthropic_base_url=lower"}
	for _, k := range AgentScrubbedVars {
		env = append(env, k+"=secret")
	}

	unix := scrubAgentEnv(append([]string(nil), env...), false)
	windows := scrubAgentEnv(append([]string(nil), env...), true)

	if want := []string{"PATH=/bin", "HOME=/home/dev", "anthropic_base_url=lower"}; !slices.Equal(unix, want) {
		t.Errorf("case-sensitive scrub = %v, want %v", unix, want)
	}
	if want := []string{"PATH=/bin", "HOME=/home/dev"}; !slices.Equal(windows, want) {
		t.Errorf("case-insensitive scrub = %v, want %v", windows, want)
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
