package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	refreshWorldFile    = "world/payments.world.yaml"
	refreshFixtureName  = "inc-1077"
	refreshFixtureRelID = "relational-sum-refunds-amount_cents-per-order_id"
)

// worldRepo is a scratch repository: a .git marker (the fixture root), the
// INC-1077 world at world/payments.world.yaml and an empty fixtures dir.
func worldRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(inc1077World)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "world"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, refreshWorldFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func inc1077Env(t *testing.T, system string) []string {
	t.Helper()
	return worldHelperEnv("ATTEST_WORLD_HELPER_MODEL=1", "ATTEST_WORLD_HELPER_INC1077=1", "ATTEST_CAPTURE_HELPER=1",
		"ATTEST_CAPTURE_HELPER_SYSTEM="+system, "OPENAI_BASE_URL="+fakeModel(t), "OPENAI_API_KEY="+e2ePlantedKey)
}

func captureWorldRepo(t *testing.T, bin, dir, system string) {
	t.Helper()
	args := append([]string{"capture", "--name", refreshFixtureName, "--out-dir", "fixtures", "--expect-fail", "--world", refreshWorldFile, "--"}, worldHelper...)
	if run := engineRun(t, bin, dir, inc1077Env(t, system), args...); run.code != 0 {
		t.Fatalf("capture: exit %d\n%s", run.code, run.stderr)
	}
}

func refreshWorldRepo(t *testing.T, bin, dir, system string, extra ...string) replayRun {
	t.Helper()
	args := append([]string{"capture", "--refresh", refreshFixtureName, "--yes", "--out-dir", "fixtures"}, extra...)
	return engineRun(t, bin, dir, inc1077Env(t, system), args...)
}

type refreshedFixture struct {
	Source struct {
		World *struct{ File string }
	}
	World        map[string]any
	Contracts    []struct{ ID string }
	Verification struct {
		Verified         bool
		ExpectedFailures []string `json:"expected_failures"`
	}
}

func readRefreshed(t *testing.T, dir string) (refreshedFixture, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "fixtures", refreshFixtureName+".attest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f refreshedFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f, raw
}

func TestCaptureRefresh_WorldFixture_PromptEditShowsDiffAndKeepsWorldAndRelationalContracts(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := worldRepo(t)
	captureWorldRepo(t, bin, dir, "You refund once.\nAlways be brief.")
	before, _ := readRefreshed(t, dir)
	if before.Source.World == nil || before.Source.World.File != refreshWorldFile {
		t.Fatalf("the fixture does not record its world source: %+v", before.Source)
	}

	run := refreshWorldRepo(t, bin, dir, "You refund twice.\nAlways be brief.")

	if run.code != 0 {
		t.Fatalf("refresh: exit %d\n%s", run.code, run.stderr)
	}
	for _, want := range []string{"refresh inc-1077: 1 of 1 exchanges changed", "system prompt:", "- You refund once.", "+ You refund twice."} {
		if !strings.Contains(run.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, run.stderr)
		}
	}
	after, _ := readRefreshed(t, dir)
	if len(after.World) == 0 || after.World["definition_sha256"] != before.World["definition_sha256"] {
		t.Errorf("world section not kept: %v", after.World)
	}
	if after.Source.World == nil || after.Source.World.File != refreshWorldFile {
		t.Errorf("world source not kept: %+v", after.Source)
	}
	found := false
	for _, c := range after.Contracts {
		found = found || c.ID == refreshFixtureRelID
	}
	if !found || !after.Verification.Verified || len(after.Verification.ExpectedFailures) < 2 {
		t.Errorf("relational contracts or --expect-fail dropped: contracts %v, verified %v, expected failures %v", after.Contracts, after.Verification.Verified, after.Verification.ExpectedFailures)
	}
}

func TestCaptureRefresh_WorldFixtureUnchanged_SaysNoSemanticChangeAndKeepsBytes(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := worldRepo(t)
	captureWorldRepo(t, bin, dir, "You refund once.")
	_, first := readRefreshed(t, dir)

	run := refreshWorldRepo(t, bin, dir, "You refund once.")

	if run.code != 0 || !strings.Contains(run.stderr, "refresh inc-1077: no semantic change") {
		t.Fatalf("exit %d\n%s", run.code, run.stderr)
	}
	if _, second := readRefreshed(t, dir); string(first) != string(second) {
		t.Error("an unchanged refresh rewrote the fixture differently")
	}
}

func TestCaptureRefresh_WorldFileChanged_RefusesAndKeepsTheFixture(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := worldRepo(t)
	captureWorldRepo(t, bin, dir, "You refund once.")
	_, first := readRefreshed(t, dir)
	path := filepath.Join(dir, refreshWorldFile)
	raw, _ := os.ReadFile(path)
	changed := strings.Replace(string(raw), "amount_cents: 12600", "amount_cents: 99999", 1)
	if changed == string(raw) {
		t.Fatal("test edit did not change the world")
	}
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}

	run := refreshWorldRepo(t, bin, dir, "You refund once.")

	if run.code != 1 || !strings.Contains(run.stderr, "definition_sha256") || !strings.Contains(run.stderr, refreshWorldFile) {
		t.Fatalf("exit %d, want 1 naming the changed world definition and file:\n%s", run.code, run.stderr)
	}
	if _, second := readRefreshed(t, dir); string(first) != string(second) {
		t.Error("a refused refresh rewrote the fixture")
	}
}

func TestCaptureRefresh_WorldFileMissing_Refuses(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := worldRepo(t)
	captureWorldRepo(t, bin, dir, "You refund once.")
	if err := os.Remove(filepath.Join(dir, refreshWorldFile)); err != nil {
		t.Fatal(err)
	}

	run := refreshWorldRepo(t, bin, dir, "You refund once.")

	if run.code != 1 || !strings.Contains(run.stderr, "missing") || !strings.Contains(run.stderr, refreshWorldFile) {
		t.Fatalf("exit %d, want 1 naming the missing world file:\n%s", run.code, run.stderr)
	}
}

func TestCaptureRefresh_WorldFixtureWithoutRecordedSource_Refuses(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := worldRepo(t)
	captureWorldRepo(t, bin, dir, "You refund once.")
	path := filepath.Join(dir, "fixtures", refreshFixtureName+".attest.json")
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc["source"].(map[string]any), "world")
	out, _ := json.Marshal(doc)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}

	run := refreshWorldRepo(t, bin, dir, "You refund once.")

	if run.code != 1 || !strings.Contains(run.stderr, "does not record") {
		t.Fatalf("exit %d, want 1 saying the world source is not recorded:\n%s", run.code, run.stderr)
	}
}

func TestCaptureJSON_RelationalCapped_MatchesTheSchema(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	var order, refund strings.Builder
	for i := 0; i < 55; i++ {
		fmt.Fprintf(&order, ", f%04d: 10", i)
		fmt.Fprintf(&refund, ", f%04d: 1", i)
	}
	world := fmt.Sprintf(`world_version: 1
base_url_env: PAYMENTS_BASE_URL
seed: 1
resources:
  orders:
    path: /orders
    id_field: order_id
    routes: [get]
    seed:
      - {order_id: O-1%s}
  refunds:
    path: /refunds
    id_field: refund_id
    routes: [list]
    seed:
      - {refund_id: R-1, order_id: O-1%s}
`, order.String(), refund.String())
	worldPath := filepath.Join(dir, "capped.world.yaml")
	if err := os.WriteFile(worldPath, []byte(world), 0o644); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"capture", "--json", "--name", "capped", "--out-dir", filepath.Join(dir, "fixtures"), "--world", worldPath, "--"}, helperAgent...)

	run := engineRun(t, bin, dir, []string{"ATTEST_CAPTURE_HELPER=1", "OPENAI_BASE_URL=" + fakeModel(t), "OPENAI_API_KEY=" + e2ePlantedKey}, args...)

	if run.code != 0 {
		t.Fatalf("exit %d\n%s", run.code, run.stderr)
	}
	if err := validateOneJSONDocument(compileCLISchema(t, "cli/capture.schema.json"), []byte(run.stdout)); err != nil {
		t.Fatalf("capped output does not match cli/capture.schema.json: %v\n%s", err, run.stdout)
	}
	var res struct {
		Capped string `json:"relational_capped"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Capped, "first 50") {
		t.Errorf("relational_capped = %q, want the cap reported", res.Capped)
	}
}

func TestCaptureRefresh_WorldFileIsASymlinkOutOfTheRepo_RefusedBeforeAnyPromptWithoutItsContent(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := worldRepo(t)
	captureWorldRepo(t, bin, dir, "You refund once.")
	secret := filepath.Join(t.TempDir(), "secret.yaml")
	if err := os.WriteFile(secret, []byte("api_key: hunter2-planted-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worldPath := filepath.Join(dir, refreshWorldFile)
	if err := os.Remove(worldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, worldPath); err != nil {
		t.Fatal(err)
	}

	// No --yes: a prompt would show "will run" first.
	run := engineRun(t, bin, dir, inc1077Env(t, "You refund once."), "capture", "--refresh", refreshFixtureName, "--out-dir", "fixtures")

	if run.code != 1 || !strings.Contains(run.stderr, "inside the repository") {
		t.Fatalf("exit %d, want 1 refusing the symlinked world:\n%s", run.code, run.stderr)
	}
	for _, leak := range []string{"hunter2", "api_key", "will run"} {
		if strings.Contains(run.stderr+run.stdout, leak) {
			t.Errorf("output contains %q:\n%s", leak, run.stderr)
		}
	}
}

func TestCapture_WorldNamedThroughASymlinkedParent_RecordsTheRepoRelativeFile(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := worldRepo(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"capture", "--name", refreshFixtureName, "--out-dir", filepath.Join(dir, "fixtures"), "--expect-fail", "--world", filepath.Join(link, refreshWorldFile), "--"}, worldHelper...)

	run := engineRun(t, bin, dir, inc1077Env(t, "You refund once."), args...)

	if run.code != 0 {
		t.Fatalf("capture: exit %d\n%s", run.code, run.stderr)
	}
	got, _ := readRefreshed(t, dir)
	if got.Source.World == nil || got.Source.World.File != refreshWorldFile {
		t.Errorf("source.world = %+v, want file %s", got.Source.World, refreshWorldFile)
	}
}
