package main_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/attest-framework/attest/engine/pkg/types"
)

// TestDoctor_InvalidNumericEnv_ExitsOneNamingEachVariable covers the numeric
// settings that must be positive integers: every offender is named, in one run.
func TestDoctor_InvalidNumericEnv_ExitsOneNamingEachVariable(t *testing.T) {
	bin := buildEngineBinary(t)
	env := engineEnv(t.TempDir(),
		"ATTEST_EVAL_CONCURRENCY=-3", "ATTEST_HISTORY_MAX_ROWS=-7", "ATTEST_JUDGE_TIMEOUT_S=banana",
		"ATTEST_EMBEDDING_CACHE_MAX_MB=0")

	stdout, stderr, code := runDoctor(t, bin, env)

	if code != 1 {
		t.Fatalf("exit %d, want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	for _, name := range []string{
		"ATTEST_EVAL_CONCURRENCY", "ATTEST_HISTORY_MAX_ROWS", "ATTEST_JUDGE_TIMEOUT_S", "ATTEST_EMBEDDING_CACHE_MAX_MB",
	} {
		if !strings.Contains(stderr, name) {
			t.Errorf("stderr does not name %s:\n%s", name, stderr)
		}
	}
}

func TestDoctor_ValidNumericEnv_ExitsZero(t *testing.T) {
	bin := buildEngineBinary(t)
	env := engineEnv(t.TempDir(), "ATTEST_EVAL_CONCURRENCY=4", "ATTEST_JUDGE_TIMEOUT_S=30", "ATTEST_HISTORY_MAX_ROWS=1000")

	if _, stderr, code := runDoctor(t, bin, env, "--json"); code != 0 {
		t.Fatalf("exit %d, want 0\nstderr: %s", code, stderr)
	}
}

func TestEngine_InvalidNumericEnv_RefusesToStart(t *testing.T) {
	bin := buildEngineBinary(t)
	cmd := exec.Command(bin)
	cmd.Env = engineEnv(t.TempDir(), "ATTEST_EVAL_CONCURRENCY=-3")
	var stderr strings.Builder
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 || !strings.Contains(stderr.String(), "ATTEST_EVAL_CONCURRENCY") {
		t.Fatalf("err %v stderr %q, want exit 1 naming ATTEST_EVAL_CONCURRENCY", err, stderr.String())
	}
}

// TestEngine_SigtermDuringJudgeCall_ExitsZeroWithinTwoSeconds starts a judge
// call against a provider that never answers and signals the engine.
func TestEngine_SigtermDuringJudgeCall_ExitsZeroWithinTwoSeconds(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			received := make(chan struct{}, 8)
			release := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			t.Cleanup(func() { close(release); provider.Close() })

			bin := buildEngineBinary(t)
			cmd := exec.Command(bin, "--log-level=error")
			cmd.Env = engineEnv(t.TempDir(),
				"ATTEST_JUDGE_PROVIDER=openai", "ATTEST_JUDGE_BASE_URL="+provider.URL+"/v1",
				"ATTEST_JUDGE_API_KEY=test-key", "ATTEST_JUDGE_MODEL=judge-model", "ATTEST_JUDGE_TIMEOUT_S=60")
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(func() { _ = cmd.Process.Kill() })

			send := func(id int, method string, params any) {
				raw, _ := json.Marshal(params)
				line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(raw)})
				if _, err := stdin.Write(append(line, '\n')); err != nil {
					t.Fatal(err)
				}
			}
			send(1, "initialize", types.InitializeParams{SDKName: "lifecycle-test", SDKVersion: "0", ProtocolVersion: 1})
			if _, err := bufio.NewReader(stdout).ReadBytes('\n'); err != nil {
				t.Fatalf("initialize reply: %v", err)
			}
			send(2, "evaluate_batch", types.EvaluateBatchParams{
				Trace: types.Trace{
					SchemaVersion: 1, TraceID: "t1", AgentID: "a1",
					Input: json.RawMessage(`"hi"`), Output: json.RawMessage(`{"message":"hello"}`),
				},
				Assertions: []types.Assertion{{
					AssertionID: "judge-1", Type: types.TypeLLMJudge,
					Spec: json.RawMessage(`{"target":"output.message","criteria":"helpful"}`),
				}},
			})
			select {
			case <-received:
			case <-time.After(10 * time.Second):
				t.Fatal("judge provider never received the call")
			}

			start := time.Now()
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-exited:
				if err != nil {
					t.Fatalf("engine exited with %v after %s, want exit 0", err, time.Since(start))
				}
				if d := time.Since(start); d > 2*time.Second {
					t.Fatalf("engine took %s to exit, want <= 2s", d)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("engine still running 5s after the signal")
			}
		})
	}
}
