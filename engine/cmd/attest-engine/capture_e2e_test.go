package main_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The capture e2e tests run the engine binary on a scripted agent: this test
// binary re-executed with ATTEST_CAPTURE_HELPER=1. The agent calls the model
// through OPENAI_BASE_URL (a fake upstream run by the test) and exports its
// spans to OTEL_EXPORTER_OTLP_TRACES_ENDPOINT. Ids, timestamps and the model's
// response ids change on every run, as in a real run.
const (
	e2ePlantedKey   = "sk-test-e2eplanted0123456789abcdef"
	e2ePlantedEmail = "pat.customer@example.org"
)

func TestCaptureHelperAgent(t *testing.T) {
	if os.Getenv("ATTEST_CAPTURE_HELPER") != "1" {
		t.Skip("helper process for the capture e2e tests")
	}
	if err := runScriptedAgent(os.Getenv("OPENAI_BASE_URL"), os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"), os.Getenv("OPENAI_API_KEY")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}

// runScriptedAgent makes one model call and exports the INC-1042 shape: a
// lookup and two refunds of the same order.
func runScriptedAgent(openaiBase, tracesEndpoint, apiKey string) error {
	return runScriptedAgentNote(openaiBase, tracesEndpoint, apiKey, "key "+apiKey)
}

// runScriptedAgentNote is runScriptedAgent with the first refund's note text.
func runScriptedAgentNote(openaiBase, tracesEndpoint, apiKey, note string) error {
	// ATTEST_CAPTURE_HELPER_SYSTEM adds a system prompt to the model call.
	system := ""
	if v := os.Getenv("ATTEST_CAPTURE_HELPER_SYSTEM"); v != "" {
		system = fmt.Sprintf(`{"role":"system","content":%q},`, v)
	}
	// ATTEST_CAPTURE_HELPER_OFFLINE=1 plays an agent whose model is
	// unreachable: no model call is made, only an agent and a chat span
	// export, and the process exits 1.
	offline := os.Getenv("ATTEST_CAPTURE_HELPER_OFFLINE") == "1"
	if offline {
		return exportSpans(tracesEndpoint, note, true, true)
	}
	req, _ := http.NewRequest(http.MethodPost, openaiBase+"/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[`+system+`{"role":"user","content":"refund O-7 for `+e2ePlantedEmail+`"}]}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model call: %d", resp.StatusCode)
	}
	// ATTEST_CAPTURE_HELPER_CHAT_ONLY=1 plays a healthy run that only chats:
	// one model call, no tool, exit 0.
	return exportSpans(tracesEndpoint, note, os.Getenv("ATTEST_CAPTURE_HELPER_CHAT_ONLY") == "1", false)
}

func exportSpans(tracesEndpoint, note string, chatOnly, fail bool) error {
	traceID, root := randomHex(16), randomHex(8)
	start := time.Now().UnixNano()
	at := func(i int) string { return fmt.Sprint(start + int64(i)*int64(time.Millisecond)) }
	attr := func(k, v string) string { return fmt.Sprintf(`{"key":%q,"value":{"stringValue":%q}}`, k, v) }
	spanJSON := func(id, parent, name string, i int, attrs ...string) string {
		p := ""
		if parent != "" {
			p = fmt.Sprintf(`"parentSpanId":%q,`, parent)
		}
		return fmt.Sprintf(`{"traceId":%q,"spanId":%q,%s"name":%q,"startTimeUnixNano":%q,"endTimeUnixNano":%q,"attributes":[%s]}`,
			traceID, id, p, name, at(i), at(i+1), strings.Join(attrs, ","))
	}
	tool := func(name, args string, i int) string {
		return spanJSON(randomHex(8), root, "execute_tool "+name, i, attr("gen_ai.operation.name", "execute_tool"),
			attr("gen_ai.tool.name", name), attr("gen_ai.tool.call.id", "call_"+randomHex(12)),
			attr("gen_ai.tool.call.arguments", args), attr("gen_ai.tool.call.result", `{"ok":true}`))
	}
	spans := []string{
		spanJSON(root, "", "invoke_agent refund-desk", 0, attr("gen_ai.operation.name", "invoke_agent"), attr("gen_ai.agent.name", "refund-desk"),
			attr("gen_ai.conversation.id", fmt.Sprintf("conv-%d", time.Now().UnixNano()))),
		spanJSON(randomHex(8), root, "chat gpt-test", 1, attr("gen_ai.operation.name", "chat"), attr("gen_ai.request.model", "gpt-test")),
		tool("lookup_order", `{"order_id":"O-7"}`, 3),
		tool("issue_refund", `{"order_id":"O-7","note":"`+note+` for `+e2ePlantedEmail+`"}`, 5),
	}
	if chatOnly {
		spans = spans[:2]
	}
	// ATTEST_CAPTURE_HELPER_REFUNDS=1 plays a healthy run with one refund.
	if !chatOnly && os.Getenv("ATTEST_CAPTURE_HELPER_REFUNDS") != "1" {
		spans = append(spans, tool("issue_refund", `{"order_id":"O-7"}`, 7))
	}
	doc := `{"resourceSpans":[{"scopeSpans":[{"spans":[` + strings.Join(spans, ",") + `]}]}]}`
	resp, err := http.Post(tracesEndpoint, "application/json", strings.NewReader(doc))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("export: %d", resp.StatusCode)
	}
	if fail {
		return errors.New("model unreachable")
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// fakeModel is an OpenAI-compatible upstream whose ids and timestamps change
// on every response.
func fakeModel(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+e2ePlantedKey) {
			http.Error(w, "missing credential", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-%s","object":"chat.completion","created":%d,"model":"gpt-test","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`,
			randomHex(10), time.Now().UnixNano())
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

type captureRun struct {
	stdout, stderr string
	code           int
	fixture        []byte
}

func runCaptureCommand(t *testing.T, bin, upstream, dir string, extra ...string) captureRun {
	t.Helper()
	args := append([]string{"capture", "--name", "scripted", "--out-dir", filepath.Join(dir, "fixtures")}, extra...)
	args = append(args, "--", os.Args[0], "-test.run=^TestCaptureHelperAgent$")
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ATTEST_CAPTURE_HELPER=1", "OPENAI_BASE_URL="+upstream, "OPENAI_API_KEY="+e2ePlantedKey)
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
	fixture, _ := os.ReadFile(filepath.Join(dir, "fixtures", "scripted.attest.json"))
	return captureRun{stdout.String(), stderr.String(), code, fixture}
}

func TestCaptureCommand_IncidentTwice_ByteIdenticalRedactedVerified(t *testing.T) {
	bin := buildEngineBinary(t)
	upstream := fakeModel(t)
	dirA, dirB := t.TempDir(), t.TempDir()

	a := runCaptureCommand(t, bin, upstream, dirA, "--expect-fail", "--result-file", filepath.Join(dirA, "result.json"))
	b := runCaptureCommand(t, bin, upstream, dirB, "--expect-fail")

	if a.code != 0 || b.code != 0 {
		t.Fatalf("exit %d/%d\n%s\n%s", a.code, b.code, a.stderr, b.stderr)
	}
	if !bytes.Equal(a.fixture, b.fixture) {
		t.Fatalf("fixtures differ:\n%s\n---\n%s", a.fixture, b.fixture)
	}
	if !bytes.Contains(a.fixture, []byte(`"conversation_id": "{{conversation_1}}"`)) || bytes.Contains(a.fixture, []byte("conv-")) {
		t.Errorf("conversation id not templated:\n%s", a.fixture)
	}
	for _, secret := range []string{e2ePlantedKey, e2ePlantedEmail} {
		if bytes.Contains(a.fixture, []byte(secret)) || strings.Contains(a.stderr, secret) {
			t.Errorf("fixture or output contains %q", secret)
		}
	}
	for _, want := range []string{"cardinality-issue_refund-per-order_id", "incident reproduction", "openai chat_completions ×1"} {
		if !strings.Contains(a.stderr, want) {
			t.Errorf("summary lacks %q:\n%s", want, a.stderr)
		}
	}
	var result struct {
		Fixture  string `json:"fixture"`
		Verified bool   `json:"verified"`
	}
	data, err := os.ReadFile(filepath.Join(dirA, "result.json"))
	if err != nil || json.Unmarshal(data, &result) != nil || !result.Verified || filepath.Base(result.Fixture) != "scripted.attest.json" {
		t.Fatalf("result file %s (%v)", data, err)
	}

	fixturePath := filepath.Join(dirA, "fixtures", "scripted.attest.json")
	stdout, stderr, code := runEngine(t, bin, t.TempDir(), "trace", "check", fixturePath)
	if code != 0 || !strings.Contains(stdout, "structure: OK") {
		t.Fatalf("trace check: exit %d\n%s%s", code, stdout, stderr)
	}
	check := exec.Command(bin, "capture", "--check-redaction", fixturePath)
	check.Env = append(os.Environ(), "OPENAI_API_KEY="+e2ePlantedKey)
	out, err := check.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "clean") {
		t.Fatalf("check-redaction: %v\n%s", err, out)
	}
}

// Without --expect-fail the drafter does not know the run is an incident: it
// drafts the observed per-id maximum, so the double refund verifies.
func TestCaptureCommand_IncidentWithoutExpectFail_DraftsObservedMaxAndVerifies(t *testing.T) {
	bin := buildEngineBinary(t)

	r := runCaptureCommand(t, bin, fakeModel(t), t.TempDir())

	if r.code != 0 || !strings.Contains(r.stderr, "verified: every drafted contract passes") {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if !bytes.Contains(r.fixture, []byte(`"description": "issue_refund at most 2× per order_id (observed maximum)"`)) {
		t.Fatalf("issue_refund not drafted at its observed maximum:\n%s", r.fixture)
	}
}

// A command that exits non-zero with no model call is never green, and the
// message names every cause that holds, the empty contract list included.
func TestCaptureCommand_ExitsNonZeroWithNoModelCalls_NeverVerifiedNamesEveryCause(t *testing.T) {
	bin := buildEngineBinary(t)
	t.Setenv("ATTEST_CAPTURE_HELPER_OFFLINE", "1")

	r := runCaptureCommand(t, bin, fakeModel(t), t.TempDir())

	if r.code != 1 {
		t.Fatalf("exit %d, want 1\n%s", r.code, r.stderr)
	}
	if strings.Contains(r.stderr, "  verified:") || strings.Contains(r.stderr, "captured: nothing to verify") {
		t.Fatalf("false green:\n%s", r.stderr)
	}
	for _, want := range []string{"not verified: ", "no model calls were recorded", "the command exited 3", "no contracts were drafted"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("summary lacks %q:\n%s", want, r.stderr)
		}
	}
	if !bytes.Contains(r.fixture, []byte(`"verified": false`)) {
		t.Fatalf("fixture not recorded as unverified:\n%s", r.fixture)
	}
}

// A healthy chat-only run drafts nothing: the fixture is written unverified,
// the capture exits 0 and never says "verified".
func TestCaptureCommand_ChatOnlyRun_NothingToVerifyExitsZero(t *testing.T) {
	bin := buildEngineBinary(t)
	t.Setenv("ATTEST_CAPTURE_HELPER_CHAT_ONLY", "1")
	dir := t.TempDir()
	resultFile := filepath.Join(dir, "result.json")

	r := runCaptureCommand(t, bin, fakeModel(t), dir, "--result-file", resultFile)

	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "captured: nothing to verify (no tool call, structured output or cost); replay-only fixture, no test drafted") {
		t.Fatalf("summary:\n%s", r.stderr)
	}
	if strings.Contains(r.stderr, "  verified:") || strings.Contains(r.stderr, "not verified") {
		t.Fatalf("summary claims a verification result:\n%s", r.stderr)
	}
	if !bytes.Contains(r.fixture, []byte(`"verified": false`)) {
		t.Fatalf("fixture:\n%s", r.fixture)
	}
	data, err := os.ReadFile(resultFile)
	if err != nil || !strings.Contains(string(data), `"outcome":"nothing_to_verify"`) {
		t.Fatalf("result file %s (%v)", data, err)
	}
}

// --json carries the outcome; a chat-only run exits 0 with nothing_to_verify.
func TestCaptureCommand_JSON_ChatOnlyReportsNothingToVerify(t *testing.T) {
	bin := buildEngineBinary(t)
	t.Setenv("ATTEST_CAPTURE_HELPER_CHAT_ONLY", "1")

	r := runCaptureCommand(t, bin, fakeModel(t), t.TempDir(), "--json")

	if r.code != 0 || !strings.Contains(r.stdout, `"outcome": "nothing_to_verify"`) || !strings.Contains(r.stdout, `"verified": false`) {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
}

// --expect-fail on a run where the incident did not happen leaves the fixture
// unverified and exits 1.
func TestCaptureCommand_ExpectFailWithoutIncident_ExitsOneUnverified(t *testing.T) {
	bin := buildEngineBinary(t)
	t.Setenv("ATTEST_CAPTURE_HELPER_REFUNDS", "1")

	r := runCaptureCommand(t, bin, fakeModel(t), t.TempDir(), "--expect-fail")

	if r.code != 1 || !strings.Contains(r.stderr, "not verified: --expect-fail given but every drafted contract passes") {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if !bytes.Contains(r.fixture, []byte(`"verified": false`)) {
		t.Fatalf("unverified fixture not written:\n%s", r.fixture)
	}
}

func TestCaptureCheckRedaction_PlantedKey_ExitsOneWithoutPrintingIt(t *testing.T) {
	bin := buildEngineBinary(t)
	path := filepath.Join(t.TempDir(), "leak.attest.json")
	os.WriteFile(path, []byte(`{"a":{"note":"key `+e2ePlantedKey+`"}}`), 0o644)
	cmd := exec.Command(bin, "capture", "--check-redaction", path)
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+e2ePlantedKey)

	out, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 || !strings.Contains(string(out), "$.a.note") || strings.Contains(string(out), e2ePlantedKey) {
		t.Fatalf("err %v\n%s", err, out)
	}
}

func TestCaptureServe_FinalizeWritesFixture_EOFAborts(t *testing.T) {
	bin := buildEngineBinary(t)
	upstream := fakeModel(t)
	for _, finalize := range []bool{true, false} {
		dir := t.TempDir()
		cmd := exec.Command(bin, "capture", "--serve", "--expect-fail", "--name", "inproc", "--out-dir", dir)
		cmd.Env = append(os.Environ(), "OPENAI_BASE_URL="+upstream)
		stdin, _ := cmd.StdinPipe()
		stdoutPipe, _ := cmd.StdoutPipe()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		lines := bufio.NewScanner(stdoutPipe)
		if !lines.Scan() {
			t.Fatalf("no ready line: %s", stderr.String())
		}
		var ready struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal(lines.Bytes(), &ready); err != nil {
			t.Fatalf("ready line %q: %v", lines.Text(), err)
		}
		if err := runScriptedAgent(ready.Env["OPENAI_BASE_URL"], ready.Env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"], e2ePlantedKey); err != nil {
			t.Fatal(err)
		}
		if finalize {
			io.WriteString(stdin, "finalize\n")
		}
		stdin.Close()
		rest, _ := io.ReadAll(stdoutPipe)
		err := cmd.Wait()
		_, statErr := os.Stat(filepath.Join(dir, "inproc.attest.json"))

		if finalize && (err != nil || statErr != nil || !strings.Contains(string(rest), `"verified":true`)) {
			t.Fatalf("finalize: %v %v %s\n%s", err, statErr, rest, stderr.String())
		}
		if !finalize && (err == nil || statErr == nil || !strings.Contains(stderr.String(), "aborted")) {
			t.Fatalf("abort: %v %v\n%s", err, statErr, stderr.String())
		}
	}
}

func TestCapture_UsageErrors_ExitTwo(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, args := range [][]string{
		{"capture"},
		{"capture", "--name", "x"},
		{"capture", "--name", "../x", "--", "true"},
		{"capture", "--name", "x", "--from", "a.json", "--", "true"},
		{"capture", "--name", "x", "--serve", "--from", "a.json"},
		{"capture", "--check-redaction", "f.json", "--name", "x"},
		{"capture", "--bogus"},
	} {
		_, stderr, code := runEngine(t, bin, t.TempDir(), args...)
		if code != 2 || !strings.Contains(stderr, "usage: attest-engine capture") {
			t.Errorf("%v: exit %d\n%s", args, code, stderr)
		}
	}
}

func TestCaptureFrom_IncidentOTLP_TraceOnlyFixture(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	otlp, _ := filepath.Abs("../../../protocol-tests/trace-v2/otlp/inc-1042-refund.otlp.json")

	stdout, stderr, code := runEngine(t, bin, t.TempDir(), "capture", "--name", "inc-1042", "--expect-fail", "--from", otlp, "--out-dir", dir)

	if code != 0 || !strings.Contains(stdout, "trace-only fixture; not replay-capable") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "inc-1042.attest.json"))
	if !bytes.Contains(data, []byte(`"kind": "otlp_file"`)) || !bytes.Contains(data, []byte(`"replay_capable": false`)) {
		t.Fatalf("fixture source:\n%s", data)
	}
}

func TestCaptureCommand_SIGTERM_ForwardedToChild(t *testing.T) {
	bin := buildEngineBinary(t)
	cmd := exec.Command(bin, "capture", "--name", "x", "--out-dir", t.TempDir(), "--",
		"sh", "-c", `trap 'exit 7' TERM; echo ready; while :; do sleep 0.1; done`)
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if !bufio.NewScanner(stdout).Scan() {
		t.Fatal("child never started")
	}

	cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("engine did not finish after SIGTERM")
	}
	if !strings.Contains(stderr.String(), "command exited 7") {
		t.Fatalf("child did not get SIGTERM:\n%s", stderr.String())
	}
}

func TestCaptureFrom_ToolNameWithESC_SummaryEscaped(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	src, _ := os.ReadFile("../../../protocol-tests/trace-v2/otlp/inc-1042-refund.otlp.json")
	hostile := filepath.Join(dir, "esc.otlp.json")
	os.WriteFile(hostile, bytes.ReplaceAll(src, []byte(`"issue_refund"`), []byte(`"issue\u001b[31mrefund"`)), 0o644)

	stdout, stderr, code := runEngine(t, bin, t.TempDir(), "capture", "--name", "esc", "--expect-fail", "--from", hostile, "--out-dir", dir)

	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if strings.ContainsRune(stdout+stderr, 0x1b) || !strings.Contains(stdout, `issue\u001b[31mrefund`) {
		t.Fatalf("summary not escaped:\n%q", stdout)
	}
	out, _, _ := runEngine(t, bin, t.TempDir(), "trace", "check", filepath.Join(dir, "esc.attest.json"))
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("trace check output carries ESC: %q", out)
	}
}

func TestCaptureCheckRedaction_KeySplitAcrossDeltas_ExitsOne(t *testing.T) {
	bin := buildEngineBinary(t)
	var body strings.Builder
	for i := 0; i < len(e2ePlantedKey); i += 6 {
		part := e2ePlantedKey[i:min(i+6, len(e2ePlantedKey))]
		body.WriteString(`data: {"choices":[{"index":0,"delta":{"content":"` + part + `"}}]}` + "\n\n")
	}
	doc, _ := json.Marshal(map[string]any{"exchanges": []any{map[string]any{
		"response": map[string]any{"content_type": "text/event-stream", "body": body.String()},
	}}})
	path := filepath.Join(t.TempDir(), "split.attest.json")
	os.WriteFile(path, doc, 0o644)
	cmd := exec.Command(bin, "capture", "--check-redaction", path)
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+e2ePlantedKey)

	out, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 || !strings.Contains(string(out), `stream "choice 0 content"`) || strings.Contains(string(out), e2ePlantedKey) {
		t.Fatalf("err %v\n%s", err, out)
	}
}

// A secret the block loads into its own environment after the engine started
// (a dotenv file) reaches the engine only in the finalize line.
func TestCaptureServe_SecretInFinalizeEnviron_Redacted(t *testing.T) {
	bin := buildEngineBinary(t)
	const late = "late-loaded-value-7f3a9c"
	dir := t.TempDir()
	cmd := exec.Command(bin, "capture", "--serve", "--expect-fail", "--name", "late", "--out-dir", dir)
	cmd.Env = append(os.Environ(), "OPENAI_BASE_URL="+fakeModel(t))
	stdin, _ := cmd.StdinPipe()
	stdoutPipe, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdoutPipe)
	if !lines.Scan() {
		t.Fatalf("no ready line: %s", stderr.String())
	}
	var ready struct {
		Env map[string]string `json:"env"`
	}
	json.Unmarshal(lines.Bytes(), &ready)
	if err := runScriptedAgentNote(ready.Env["OPENAI_BASE_URL"], ready.Env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"], e2ePlantedKey, late); err != nil {
		t.Fatal(err)
	}

	finalize, _ := json.Marshal(map[string]any{"finalize": true, "environ": []string{"DOTENV_SERVICE_TOKEN=" + late, "HOME=/x"}})
	stdin.Write(append(finalize, '\n'))
	stdin.Close()
	io.ReadAll(stdoutPipe)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "late.attest.json"))

	if bytes.Contains(data, []byte(late)) || !bytes.Contains(data, []byte(`"note": "[redacted] for {{email_1}}"`)) {
		t.Fatalf("late secret not redacted:\n%s", data)
	}
}
