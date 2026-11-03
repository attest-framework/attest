package main_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The replay e2e tests capture the scripted agent once, then replay it with
// the upstream gone: its address is a closed port, so a replay that reached
// the network would fail the agent.
const deadModel = "http://127.0.0.1:1/v1"

type replayRun struct {
	stdout, stderr string
	code           int
}

func engineRun(t *testing.T, bin, dir string, env []string, args ...string) replayRun {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = exitErr.ExitCode()
	}
	return replayRun{stdout.String(), stderr.String(), code}
}

var helperAgent = []string{os.Args[0], "-test.run=^TestCaptureHelperAgent$"}

func replayEnv(system string) []string {
	return []string{"ATTEST_CAPTURE_HELPER=1", "ATTEST_CAPTURE_HELPER_SYSTEM=" + system, "OPENAI_BASE_URL=" + deadModel, "OPENAI_API_KEY=" + e2ePlantedKey}
}

func replayArgs(dir string, extra ...string) []string {
	args := append([]string{"replay", "--name", "scripted", "--fixtures-dir", filepath.Join(dir, "fixtures")}, extra...)
	return append(append(args, "--"), helperAgent...)
}

func captureWithSystem(t *testing.T, bin, dir, system string) {
	t.Helper()
	args := append([]string{"capture", "--name", "scripted", "--out-dir", filepath.Join(dir, "fixtures"), "--expect-fail", "--"}, helperAgent...)
	run := engineRun(t, bin, dir, append(replayEnv(system), "OPENAI_BASE_URL="+fakeModel(t)), args...)
	if run.code != 0 {
		t.Fatalf("capture: exit %d\n%s", run.code, run.stderr)
	}
}

func TestReplayCommand_CapturedRun_ServedWithZeroLiveCalls(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "You refund once.")

	run := engineRun(t, bin, dir, replayEnv("You refund once."), replayArgs(dir)...)

	if run.code != 0 {
		t.Fatalf("exit %d\n%s", run.code, run.stderr)
	}
	if !strings.Contains(run.stderr, "1 replayed · 0 live model calls · $0.00") {
		t.Fatalf("summary:\n%s", run.stderr)
	}
}

func TestReplayCommand_SystemPromptEdited_StrictMissNamesChangeAndRefresh(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "You refund once.")

	run := engineRun(t, bin, dir, replayEnv("You refund twice."), replayArgs(dir)...)

	if run.code != 1 {
		t.Fatalf("exit %d\n%s", run.code, run.stderr)
	}
	want := "replay miss: system prompt changed (hash "
	suffix := "); re-record with `attest capture --refresh scripted`"
	if !strings.Contains(run.stderr, want) || !strings.Contains(run.stderr, suffix) {
		t.Fatalf("summary:\n%s", run.stderr)
	}
	if !strings.Contains(run.stderr, "0 live model calls · $0.00") {
		t.Fatalf("a miss made a live call:\n%s", run.stderr)
	}
}

func TestReplayCommand_AutoModeWithoutKey_ReportsMissWithoutGoingLive(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "You refund once.")
	env := append(replayEnv("You refund twice."), "OPENAI_API_KEY=", "ATTEST_JUDGE_API_KEY=")

	run := engineRun(t, bin, dir, env, replayArgs(dir, "--mode", "auto")...)

	if run.code != 1 || !strings.Contains(run.stderr, "replay miss: system prompt changed") || !strings.Contains(run.stderr, "0 live model calls") {
		t.Fatalf("exit %d\n%s", run.code, run.stderr)
	}
}

func TestReplayServe_StopPrintsSummaryLine(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "You refund once.")
	cmd := exec.Command(bin, "replay", "--name", "scripted", "--fixtures-dir", filepath.Join(dir, "fixtures"), "--serve")
	cmd.Env = append(os.Environ(), "OPENAI_BASE_URL="+deadModel)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	if !lines.Scan() {
		t.Fatalf("no ready line: %s", stderr.String())
	}
	var ready struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(lines.Bytes(), &ready); err != nil || ready.Env["OPENAI_BASE_URL"] == "" {
		t.Fatalf("ready line %q: %v", lines.Text(), err)
	}
	t.Setenv("ATTEST_CAPTURE_HELPER_SYSTEM", "You refund once.")
	if err := runScriptedAgent(ready.Env["OPENAI_BASE_URL"], ready.Env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"], e2ePlantedKey); err != nil {
		t.Fatal(err)
	}

	io.WriteString(stdin, "stop\n")
	if !lines.Scan() {
		t.Fatalf("no summary line: %s", stderr.String())
	}
	var out struct {
		Summary struct {
			Replayed, Live, Misses int
			Mode, Fixture          string
		} `json:"summary"`
	}
	if err := json.Unmarshal(lines.Bytes(), &out); err != nil {
		t.Fatalf("summary line %q: %v", lines.Text(), err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v\n%s", err, stderr.String())
	}
	if s := out.Summary; s.Replayed != 1 || s.Live != 0 || s.Misses != 0 || s.Mode != "strict" || s.Fixture != "scripted" {
		t.Fatalf("summary = %+v", s)
	}
}

func TestCaptureRefresh_RecapturesWithOriginalFlagsAndNewPrompt(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "You refund once.")
	path := filepath.Join(dir, "fixtures", "scripted.attest.json")
	before, _ := os.ReadFile(path)

	refresh := engineRun(t, bin, dir, append(replayEnv("You refund twice."), "OPENAI_BASE_URL="+fakeModel(t)),
		"capture", "--refresh", "scripted", "--out-dir", filepath.Join(dir, "fixtures"))

	if refresh.code != 0 {
		t.Fatalf("refresh: exit %d\n%s", refresh.code, refresh.stderr)
	}
	after, _ := os.ReadFile(path)
	if bytes.Equal(before, after) {
		t.Fatal("refresh left the fixture unchanged")
	}
	var f struct {
		Verification struct {
			ExpectFail bool `json:"expect_fail"`
		}
	}
	if err := json.Unmarshal(after, &f); err != nil || !f.Verification.ExpectFail {
		t.Fatalf("--expect-fail was not carried over: %v", err)
	}
	if run := engineRun(t, bin, dir, replayEnv("You refund twice."), replayArgs(dir)...); run.code != 0 {
		t.Fatalf("replay of the refreshed fixture: exit %d\n%s", run.code, run.stderr)
	}
}

func TestCaptureRefresh_UnrefreshableFixtures_Refused(t *testing.T) {
	bin := buildEngineBinary(t)
	cases := map[string]struct {
		edit func(src map[string]any)
		want string
	}{
		"redacted argv": {func(src map[string]any) { src["command"] = []any{"agent", "--token=[redacted]"} }, "redacted arguments"},
		"in process":    {func(src map[string]any) { src["kind"], src["command"] = "in_process", nil }, "not from a command"},
		"trace only":    {func(src map[string]any) { src["kind"], src["command"] = "otlp_file", nil }, "not from a command"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			captureWithSystem(t, bin, dir, "s")
			path := filepath.Join(dir, "fixtures", "scripted.attest.json")
			data, _ := os.ReadFile(path)
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			c.edit(doc["source"].(map[string]any))
			data, _ = json.Marshal(doc)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			run := engineRun(t, bin, dir, replayEnv("s"), "capture", "--refresh", "scripted", "--out-dir", filepath.Join(dir, "fixtures"))

			if run.code != 1 || !strings.Contains(run.stderr, c.want) {
				t.Fatalf("exit %d\n%s", run.code, run.stderr)
			}
		})
	}
}

func TestReplay_UsageErrors_ExitTwo(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, args := range [][]string{
		{"replay"},
		{"replay", "--name", "x", "--fixture", "y", "--serve"},
		{"replay", "--name", "x", "--mode", "loose", "--serve"},
		{"replay", "--name", "x"},
		{"replay", "--name", "../x", "--serve"},
		{"capture", "--refresh", "x", "--name", "y"},
	} {
		if run := engineRun(t, bin, t.TempDir(), nil, args...); run.code != 2 {
			t.Errorf("%v: exit %d\n%s", args, run.code, run.stderr)
		}
	}
}

func TestReplay_MissingFixture_ExitOne(t *testing.T) {
	bin := buildEngineBinary(t)
	run := engineRun(t, bin, t.TempDir(), nil, "replay", "--name", "absent", "--serve")
	if run.code != 1 || !strings.Contains(run.stderr, "absent.attest.json") {
		t.Fatalf("exit %d\n%s", run.code, run.stderr)
	}
}
