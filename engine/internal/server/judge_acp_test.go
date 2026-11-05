package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	llmproviders "github.com/attest-framework/attest/engine/internal/llm/providers"
	"github.com/attest-framework/attest/engine/internal/llm/providers/acptest"
	"github.com/attest-framework/attest/engine/pkg/types"
)

// TestMain lets this test binary act as the stub ACP agent named by
// ATTEST_JUDGE_ACP_COMMAND: started with a scenario flag (acptest.Command) it
// serves the scenario on stdio instead of running tests.
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

func configureACPJudge(t *testing.T, scenario string) {
	t.Helper()
	withoutProviders(t, "")
	for _, k := range llmproviders.CIEnvVars {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("ATTEST_JUDGE_PROVIDER", "acp")
	t.Setenv("ATTEST_JUDGE_ACP_COMMAND", acptest.Command(scenario, ""))
}

func TestHandler_ACPJudge_StubAgentEvaluatesL6(t *testing.T) {
	configureACPJudge(t, acptest.Judge)

	initRes, r := evaluateJudgeOnly(t)

	if !strings.Contains(strings.Join(initRes.Capabilities, ","), "layers_5_6") {
		t.Errorf("capabilities %v missing layers_5_6 with the acp judge configured", initRes.Capabilities)
	}
	if r.Status != types.StatusPass || r.Score != 0.95 {
		t.Fatalf("status = %q score = %v (explanation %q), want pass 0.95 from the stub agent", r.Status, r.Score, r.Explanation)
	}
	if r.Judge == nil || r.Judge.Provider != "acp" || !strings.HasPrefix(r.Judge.Model, "acp:") {
		t.Errorf("judge_metadata = %+v, want provider acp and an acp:<command> model label", r.Judge)
	}
}

func TestHandler_ACPJudge_AgentFailureIsNotEvaluatedProviderUnavailable(t *testing.T) {
	configureACPJudge(t, acptest.ExitEarly)

	_, r := evaluateJudgeOnly(t)

	if r.Status != types.StatusNotEvaluated || r.Reason != types.ReasonProviderUnavailable || r.FailureClass != "" {
		t.Fatalf("status = %q reason = %q failure_class = %q, want not_evaluated/provider_unavailable",
			r.Status, r.Reason, r.FailureClass)
	}
}

func TestBuildProviders_ACPJudgeWithCI_FailsWithExplanation(t *testing.T) {
	configureACPJudge(t, acptest.Judge)
	t.Setenv("CI", "true")

	_, err := buildProviders(slog.New(slog.DiscardHandler))

	if err == nil {
		t.Fatal("buildProviders accepted ATTEST_JUDGE_PROVIDER=acp with CI=true")
	}
	for _, want := range []string{"ATTEST_JUDGE_PROVIDER=acp", "CI is set", "local use only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestResolveJudgeConfig_ACPRejectsBaseURL(t *testing.T) {
	configureACPJudge(t, acptest.Judge)
	t.Setenv("ATTEST_JUDGE_BASE_URL", "https://example.com")

	_, err := resolveJudgeConfig()

	if err == nil || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("err = %v, want ATTEST_JUDGE_BASE_URL rejected for acp", err)
	}
}

func TestJudgeConfig_CacheEndpoint_ScopesACPByCommand(t *testing.T) {
	acp := judgeConfig{provider: "acp", acpCommand: "claude-code-acp"}
	openai := judgeConfig{provider: "openai", baseURL: "https://api.openai.com/v1"}

	if acp.cacheEndpoint() != "acp:claude-code-acp" || openai.cacheEndpoint() != "openai|"+openai.baseURL {
		t.Errorf("cache endpoints = %q, %q", acp.cacheEndpoint(), openai.cacheEndpoint())
	}
}

func TestBuildProviders_ACPWarning_LogsOnlyExecutableName(t *testing.T) {
	configureACPJudge(t, acptest.Judge)
	t.Setenv("ATTEST_JUDGE_ACP_COMMAND", os.Args[0]+" --api-token=SECRET-ARG")
	var logs bytes.Buffer

	_, err := buildProviders(slog.New(slog.NewTextHandler(&logs, nil)))

	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "not sandboxed") || !strings.Contains(logs.String(), filepath.Base(os.Args[0])) {
		t.Errorf("startup warning missing or without the executable name:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "SECRET-ARG") || strings.Contains(logs.String(), filepath.Dir(os.Args[0])) {
		t.Errorf("startup log exposes the ACP command line:\n%s", logs.String())
	}
}
