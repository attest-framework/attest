package providers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

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

func newStubACP(t *testing.T, scenario string) *ACP {
	t.Helper()
	t.Setenv("CI", "")
	t.Setenv(acptest.StubEnv, scenario)
	p, err := NewACP(os.Args[0], "")
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
	t.Setenv("ATTEST_JUDGE_API_KEY", "secret-key-for-another-provider")

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

func TestNewACP_CISet_RefusesWithExplanation(t *testing.T) {
	t.Setenv("CI", "true")

	_, err := NewACP(os.Args[0], "")

	if err == nil {
		t.Fatal("NewACP succeeded with CI set")
	}
	for _, want := range []string{"CI is set", "local use only", "anthropic"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestNewACP_InvalidCommand_Errors(t *testing.T) {
	t.Setenv("CI", "")
	cases := map[string]string{
		"":                             "requires ATTEST_JUDGE_ACP_COMMAND",
		"   ":                          "requires ATTEST_JUDGE_ACP_COMMAND",
		"attest-no-such-acp-agent --x": "not an executable on PATH",
	}
	for command, want := range cases {
		_, err := NewACP(command, "")

		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("NewACP(%q) err = %v, want %q", command, err, want)
		}
	}
}

func TestNewACP_ModelLabel_RecordsConfiguredModel(t *testing.T) {
	t.Setenv("CI", "")

	p, err := NewACP(os.Args[0]+" --flag", "claude-subscription")

	if err != nil {
		t.Fatal(err)
	}
	if p.DefaultModel() != "claude-subscription" || len(p.argv) != 2 {
		t.Errorf("model %q argv %v", p.DefaultModel(), p.argv)
	}
}
