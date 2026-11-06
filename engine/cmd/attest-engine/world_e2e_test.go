package main_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/attest-framework/attest/engine/internal/world"
)

const worldYAML = "../../internal/world/testdata/payments.world.yaml"

// TestWorldHelperClient is the agent the world e2e tests run: it refunds
// O-1042 twice without an idempotency key, the first time with a client
// timeout shorter than the fault's hold, as the INC-1042 agent does. With
// ATTEST_WORLD_HELPER_MODEL=1 it also makes the scripted model call and
// exports spans, so capture and replay have a run to record.
func TestWorldHelperClient(t *testing.T) {
	if os.Getenv("ATTEST_WORLD_HELPER") != "1" {
		t.Skip("helper process for the world e2e tests")
	}
	if os.Getenv("ATTEST_WORLD_HELPER_MODEL") == "1" {
		if err := runScriptedAgent(os.Getenv("OPENAI_BASE_URL"), os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"), os.Getenv("OPENAI_API_KEY")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
	}
	base := os.Getenv("PAYMENTS_BASE_URL")
	if os.Getenv("ATTEST_WORLD_HELPER_EMAIL") == "1" {
		note := `{"order_id":"O-1042","message":"refund sent to ` + e2ePlantedEmail + `"}`
		resp, err := http.Post(base+"/notifications", "application/json", strings.NewReader(note))
		if err != nil || resp.StatusCode != http.StatusCreated {
			fmt.Fprintf(os.Stderr, "notification: %v %v\n", err, resp)
			os.Exit(3)
		}
		resp.Body.Close()
	}
	if os.Getenv("ATTEST_WORLD_HELPER_INC1077") == "1" {
		postJSON(base+"/refunds", `{"order_id":"O-2077","amount_cents":1200}`)
		postJSON(base+"/refunds", `{"order_id":"O-2077","amount_cents":12600}`)
		postJSON(base+"/notifications", `{"order_id":"O-2077","customer_id":"C-0311"}`)
		os.Exit(0)
	}
	body := `{"order_id":"O-1042","amount_cents":4999}`
	for i, timeout := range []time.Duration{300 * time.Millisecond, 5 * time.Second} {
		c := &http.Client{Timeout: timeout}
		resp, err := c.Post(base+"/refunds", "application/json", strings.NewReader(body))
		if err == nil {
			resp.Body.Close()
		}
		if (i == 0) != (err != nil) {
			fmt.Fprintf(os.Stderr, "request %d: err=%v; only the first should time out\n", i+1, err)
			os.Exit(3)
		}
	}
	os.Exit(0)
}

// postJSON posts a body to the world and exits 3 unless it answers 201.
func postJSON(url, body string) {
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusCreated {
		fmt.Fprintf(os.Stderr, "post %s: %v %v\n", url, err, resp)
		os.Exit(3)
	}
	resp.Body.Close()
}

var worldHelper = []string{os.Args[0], "-test.run=^TestWorldHelperClient$"}

func worldHelperEnv(extra ...string) []string {
	return append([]string{"ATTEST_WORLD_HELPER=1"}, extra...)
}

func refundCount(t *testing.T, state []byte) int {
	t.Helper()
	if err := world.ValidateSnapshot(state); err != nil {
		t.Fatalf("snapshot rejected by world-v1: %v\n%s", err, state)
	}
	var snap world.Snapshot
	if err := json.Unmarshal(state, &snap); err != nil {
		t.Fatal(err)
	}
	return len(snap.State["refunds"])
}

func TestWorldCommand_IncidentClient_TimesOutYetCommitsAndWritesState(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "state.json")
	args := append([]string{"world", "--file", mustAbs(t, worldYAML), "--state-out", out, "--"}, worldHelper...)

	run := engineRun(t, bin, dir, worldHelperEnv(), args...)

	if run.code != 0 {
		t.Fatalf("exit %d\n%s", run.code, run.stderr)
	}
	state, _ := os.ReadFile(out)
	if n := refundCount(t, state); n != 2 {
		t.Fatalf("%d refunds, want the incident's 2\n%s", n, state)
	}
	if !strings.Contains(run.stderr, "2 refunds · 1 fault fired") {
		t.Fatalf("summary:\n%s", run.stderr)
	}
}

func TestWorldCommand_MalformedWorld_ExitsTwoNamingThePath(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "world.yaml")
	os.WriteFile(bad, []byte("world_version: 1\nbase_url_env: X_URL\nresources:\n  things: {path: /things, routes: [create, delete]}\n"), 0o644)

	run := engineRun(t, bin, dir, nil, "world", "--file", bad, "--", "true")

	if run.code != 2 {
		t.Fatalf("exit %d, want 2\n%s", run.code, run.stderr)
	}
	if !strings.Contains(run.stderr, "/resources/things/routes/1") {
		t.Fatalf("error does not name the path:\n%s", run.stderr)
	}
}

func TestWorldCommand_UsageErrors_ExitTwo(t *testing.T) {
	bin := buildEngineBinary(t)
	for name, args := range map[string][]string{
		"no file":      {"world", "--serve"},
		"neither":      {"world", "--file", mustAbs(t, worldYAML)},
		"both":         {"world", "--file", mustAbs(t, worldYAML), "--serve", "--", "true"},
		"missing file": {"world", "--file", "/nonexistent/world.yaml", "--serve"},
	} {
		if run := engineRun(t, bin, t.TempDir(), nil, args...); run.code != 2 {
			t.Errorf("%s: exit %d, want 2\n%s", name, run.code, run.stderr)
		}
	}
}

func TestWorldServe_StateEndpointThenStopPrintsFinalSnapshot(t *testing.T) {
	bin := buildEngineBinary(t)
	cmd := exec.Command(bin, "world", "--file", mustAbs(t, worldYAML), "--serve")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	rd := bufio.NewReader(stdout)
	line, err := rd.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var first struct{ Env map[string]string }
	if err := json.Unmarshal([]byte(line), &first); err != nil {
		t.Fatalf("env line %q: %v", line, err)
	}
	base := first.Env["PAYMENTS_BASE_URL"]
	if base == "" {
		t.Fatalf("env %v lacks PAYMENTS_BASE_URL", first.Env)
	}

	resp, err := http.Post(base+"/notifications", "application/json", strings.NewReader(`{"order_id":"O-1042","message":"hi"}`))
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("create notification: %v %v", err, resp)
	}
	resp.Body.Close()
	live, err := http.Get(base + world.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	liveState, _ := io.ReadAll(live.Body)
	live.Body.Close()
	if !strings.Contains(string(liveState), `"notification_id":"N-0001"`) {
		t.Fatalf("live state lacks the notification: %s", liveState)
	}

	fmt.Fprintln(stdin, "stop")
	final, err := rd.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var last struct{ World json.RawMessage }
	if err := json.Unmarshal([]byte(final), &last); err != nil || refundCount(t, last.World) != 0 {
		t.Fatalf("final line %q: %v", final, err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit: %v", err)
	}
}

func TestWorldServe_RefusesBrowserOriginAndForeignHosts(t *testing.T) {
	bin := buildEngineBinary(t)
	cmd := exec.Command(bin, "world", "--file", mustAbs(t, worldYAML), "--serve")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fmt.Fprintln(stdin, "stop"); _ = cmd.Wait() })
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	var first struct{ Env map[string]string }
	json.Unmarshal([]byte(line), &first)
	base := first.Env["PAYMENTS_BASE_URL"]

	req, _ := http.NewRequest(http.MethodGet, base+"/orders/O-1042", nil)
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("browser origin: %v %v", err, resp)
	}
	req, _ = http.NewRequest(http.MethodGet, base+"/orders/O-1042", nil)
	req.Host = "evil.example"
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("foreign host: %v %v", err, resp)
	}
	// Without the token the world is not reachable at all.
	root := base[:strings.LastIndex(base, "/world")]
	rootless := root[:strings.LastIndex(root, "/")]
	if resp, err := http.Get(rootless + "/world/orders/O-1042"); err != nil || resp.StatusCode != 404 {
		t.Fatalf("tokenless: %v %v", err, resp)
	}
}

func TestCaptureWorld_StoresFinalStateInTheFixture(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	args := append([]string{"capture", "--name", "scripted", "--out-dir", filepath.Join(dir, "fixtures"), "--expect-fail", "--world", mustAbs(t, worldYAML), "--"}, worldHelper...)

	run := engineRun(t, bin, dir, worldHelperEnv("ATTEST_WORLD_HELPER_MODEL=1", "ATTEST_CAPTURE_HELPER=1", "OPENAI_BASE_URL="+fakeModel(t), "OPENAI_API_KEY="+e2ePlantedKey), args...)

	if run.code != 0 {
		t.Fatalf("capture: exit %d\n%s", run.code, run.stderr)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "fixtures", "scripted.attest.json"))
	var fx struct{ World json.RawMessage }
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if string(fx.World) == "null" || refundCount(t, fx.World) != 2 {
		t.Fatalf("fixture world section: %s", fx.World)
	}
}

func TestCaptureWorld_EmailInRecord_TemplatedLikeTheTrace(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	args := append([]string{"capture", "--name", "scripted", "--out-dir", filepath.Join(dir, "fixtures"), "--expect-fail", "--world", mustAbs(t, worldYAML), "--"}, worldHelper...)

	run := engineRun(t, bin, dir, worldHelperEnv("ATTEST_WORLD_HELPER_EMAIL=1", "ATTEST_WORLD_HELPER_MODEL=1", "ATTEST_CAPTURE_HELPER=1", "OPENAI_BASE_URL="+fakeModel(t), "OPENAI_API_KEY="+e2ePlantedKey), args...)

	if run.code != 0 {
		t.Fatalf("capture: exit %d\n%s", run.code, run.stderr)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "fixtures", "scripted.attest.json"))
	if strings.Contains(string(raw), e2ePlantedEmail) {
		t.Fatal("clear-text address in the fixture file")
	}
	var fx struct {
		Trace json.RawMessage
		World json.RawMessage
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	var snap world.Snapshot
	if err := json.Unmarshal(fx.World, &snap); err != nil {
		t.Fatal(err)
	}
	notes := snap.State["notifications"]
	if len(notes) != 1 {
		t.Fatalf("world notifications: %v", notes)
	}
	msg, _ := notes[0]["message"].(string)
	token := regexp.MustCompile(`\{\{email_\d+\}\}`).FindString(msg)
	if token == "" || msg != "refund sent to "+token {
		t.Fatalf("world message %q is not templated", msg)
	}
	if !strings.Contains(string(fx.Trace), token) {
		t.Fatalf("trace does not carry the world's token %s:\n%s", token, fx.Trace)
	}
	if n := refundCount(t, fx.World); n != 2 {
		t.Fatalf("%d refunds, want 2", n)
	}
}

func TestCaptureWorld_Absent_KeepsNullSection(t *testing.T) {
	bin := buildEngineBinary(t)
	run := runCaptureCommand(t, bin, fakeModel(t), t.TempDir())
	var fx struct{ World json.RawMessage }
	if err := json.Unmarshal(run.fixture, &fx); err != nil || string(fx.World) != "null" {
		t.Fatalf("world section %s (%v), want null", fx.World, err)
	}
}

func TestCapture_WorldWithFrom_ExitsTwo(t *testing.T) {
	bin := buildEngineBinary(t)
	run := engineRun(t, bin, t.TempDir(), nil, "capture", "--name", "x", "--from", "otlp.json", "--world", mustAbs(t, worldYAML))
	if run.code != 2 {
		t.Fatalf("exit %d, want 2\n%s", run.code, run.stderr)
	}
}

func TestReplayWorld_HostsFreshWorldNextToReplayedModel(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	capArgs := append([]string{"capture", "--name", "scripted", "--out-dir", filepath.Join(dir, "fixtures"), "--expect-fail", "--world", mustAbs(t, worldYAML), "--"}, worldHelper...)
	env := worldHelperEnv("ATTEST_WORLD_HELPER_MODEL=1", "ATTEST_CAPTURE_HELPER=1", "OPENAI_API_KEY="+e2ePlantedKey)
	if run := engineRun(t, bin, dir, append(env, "OPENAI_BASE_URL="+fakeModel(t)), capArgs...); run.code != 0 {
		t.Fatalf("capture: exit %d\n%s", run.code, run.stderr)
	}

	args := append([]string{"replay", "--name", "scripted", "--fixtures-dir", filepath.Join(dir, "fixtures"), "--world", mustAbs(t, worldYAML), "--"}, worldHelper...)
	run := engineRun(t, bin, dir, append(env, "OPENAI_BASE_URL="+deadModel), args...)

	if run.code != 0 {
		t.Fatalf("replay: exit %d\n%s", run.code, run.stderr)
	}
	if !strings.Contains(run.stderr, "0 live model calls") || !strings.Contains(run.stderr, "2 refunds · 1 fault fired") {
		t.Fatalf("summary lacks replay and world lines:\n%s", run.stderr)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

const inc1077World = "../../testdata/capture/inc-1077.world.yaml"

func inc1077Capture(t *testing.T, extra ...string) (run replayRun, fixturePath string) {
	t.Helper()
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	args := append([]string{"capture", "--name", "inc-1077", "--out-dir", filepath.Join(dir, "fixtures"), "--world", mustAbs(t, inc1077World)}, extra...)
	args = append(append(args, "--"), worldHelper...)
	run = engineRun(t, bin, dir, worldHelperEnv("ATTEST_WORLD_HELPER_MODEL=1", "ATTEST_WORLD_HELPER_INC1077=1", "ATTEST_CAPTURE_HELPER=1", "OPENAI_BASE_URL="+fakeModel(t), "OPENAI_API_KEY="+e2ePlantedKey), args...)
	return run, filepath.Join(dir, "fixtures", "inc-1077.attest.json")
}

func TestCaptureWorld_ExpectFail_DraftsRelationalContractsThatFailOnTheIncident(t *testing.T) {
	run, path := inc1077Capture(t, "--expect-fail")

	if run.code != 0 {
		t.Fatalf("capture: exit %d\n%s", run.code, run.stderr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Contracts    []struct{ ID, Kind, Type string }
		Verification struct {
			Verified         bool
			ExpectedFailures []string `json:"expected_failures"`
			Results          []struct{ Contract, Status, Explanation string }
		}
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if !fx.Verification.Verified {
		t.Fatalf("fixture not verified:\n%s", run.stderr)
	}
	want := map[string]string{
		"relational-sum-refunds-amount_cents-per-order_id":          "effect violated: refunds amount_cents sum 13800 for O-2077 exceeds orders.amount_cents 12600",
		"relational-matches-notifications-customer_id-per-order_id": "effect violated: notifications customer_id C-0311 for O-2077 does not match orders.customer_id C-0744",
	}
	for _, r := range fx.Verification.Results {
		if exp, ok := want[r.Contract]; ok {
			if r.Status != "hard_fail" || !strings.Contains(r.Explanation, exp) {
				t.Errorf("%s: %s %q, want hard_fail containing %q", r.Contract, r.Status, r.Explanation, exp)
			}
			delete(want, r.Contract)
		}
	}
	for id := range want {
		t.Errorf("no result for drafted contract %s", id)
	}
	for id := range map[string]bool{"relational-sum-refunds-amount_cents-per-order_id": true, "relational-matches-notifications-customer_id-per-order_id": true} {
		found := false
		for _, e := range fx.Verification.ExpectedFailures {
			found = found || e == id
		}
		if !found {
			t.Errorf("%s is not recorded as an expected failure: %v", id, fx.Verification.ExpectedFailures)
		}
	}
	kinds := 0
	for _, c := range fx.Contracts {
		if c.Kind == "relational" && c.Type == "effect" {
			kinds++
		}
	}
	if kinds != 2 {
		t.Errorf("%d relational effect contracts stored, want 2", kinds)
	}
}

func TestCaptureWorld_WithoutExpectFail_FailingRelationalDraftsLeaveTheFixtureNotVerified(t *testing.T) {
	run, path := inc1077Capture(t)

	if run.code != 1 {
		t.Fatalf("capture: exit %d, want 1\n%s", run.code, run.stderr)
	}
	raw, _ := os.ReadFile(path)
	var fx struct{ Verification struct{ Verified bool } }
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if fx.Verification.Verified || !strings.Contains(run.stderr, "relational-sum-refunds-amount_cents-per-order_id") {
		t.Fatalf("verified=%v, stderr:\n%s", fx.Verification.Verified, run.stderr)
	}
}

func TestCaptureWorld_Recapture_IsByteIdentical(t *testing.T) {
	_, a := inc1077Capture(t, "--expect-fail")
	_, b := inc1077Capture(t, "--expect-fail")
	first, _ := os.ReadFile(a)
	second, _ := os.ReadFile(b)
	if len(first) == 0 || string(first) != string(second) {
		t.Fatalf("recaptures differ (%d vs %d bytes)", len(first), len(second))
	}
}

func TestCaptureWorld_NoRelation_DraftsTraceContractsOnly(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	args := append([]string{"capture", "--name", "scripted", "--out-dir", filepath.Join(dir, "fixtures"), "--expect-fail", "--world", mustAbs(t, worldYAML), "--"}, worldHelper...)
	run := engineRun(t, bin, dir, worldHelperEnv("ATTEST_WORLD_HELPER_MODEL=1", "ATTEST_CAPTURE_HELPER=1", "OPENAI_BASE_URL="+fakeModel(t), "OPENAI_API_KEY="+e2ePlantedKey), args...)
	if run.code != 0 {
		t.Fatalf("capture: exit %d\n%s", run.code, run.stderr)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "fixtures", "scripted.attest.json"))
	if strings.Contains(string(raw), `"relational"`) {
		t.Fatalf("a world with no id relation drafted a relational contract")
	}
}
