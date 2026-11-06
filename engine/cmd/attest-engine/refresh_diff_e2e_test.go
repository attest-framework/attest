package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// refreshWith re-records the scripted fixture with a different system prompt
// through the fake model, as `attest capture --refresh scripted --yes`.
func refreshWith(t *testing.T, bin, dir, system string, extra ...string) replayRun {
	t.Helper()
	args := append([]string{"capture", "--refresh", "scripted", "--yes", "--out-dir", filepath.Join(dir, "fixtures")}, extra...)
	return engineRun(t, bin, dir, append(replayEnv(system), "OPENAI_BASE_URL="+fakeModel(t)), args...)
}

func TestCaptureRefresh_SystemPromptEdit_ShowsChangedLinesPerExchange(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "You refund once.\nAlways be brief.")

	run := refreshWith(t, bin, dir, "You refund twice.\nAlways be brief.")

	if run.code != 0 {
		t.Fatalf("refresh: exit %d\n%s", run.code, run.stderr)
	}
	for _, want := range []string{
		"refresh scripted: 1 of 1 exchanges changed",
		"exchange 0 (openai chat_completions gpt-test): changed",
		"system prompt:",
		"- You refund once.",
		"+ You refund twice.",
	} {
		if !strings.Contains(run.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, run.stderr)
		}
	}
	if strings.Contains(run.stderr, "Always be brief.") {
		t.Errorf("an unchanged prompt line was printed:\n%s", run.stderr)
	}
}

func TestCaptureRefresh_NoChange_SaysSoAndKeepsFixtureBytes(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "You refund once.")
	path := filepath.Join(dir, "fixtures", "scripted.attest.json")
	before, _ := os.ReadFile(path)

	run := refreshWith(t, bin, dir, "You refund once.")

	if run.code != 0 {
		t.Fatalf("refresh: exit %d\n%s", run.code, run.stderr)
	}
	if !strings.Contains(run.stderr, "refresh scripted: no semantic change") {
		t.Errorf("no-change line missing:\n%s", run.stderr)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("an unchanged refresh rewrote the fixture with different bytes")
	}
}

func TestCaptureRefresh_JSON_CarriesDiffObjectMatchingSchema(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "You refund once.")

	run := refreshWith(t, bin, dir, "You refund twice.", "--json")

	if run.code != 0 {
		t.Fatalf("refresh: exit %d\n%s", run.code, run.stderr)
	}
	if err := validateOneJSONDocument(compileCLISchema(t, "cli/capture.schema.json"), []byte(run.stdout)); err != nil {
		t.Fatalf("stdout does not match cli/capture.schema.json: %v\n%s", err, run.stdout)
	}
	var res struct {
		Diff struct {
			Changed   bool `json:"changed"`
			Exchanges []struct {
				Index  int    `json:"index"`
				Change string `json:"change"`
				System []struct {
					Op   string `json:"op"`
					Text string `json:"text"`
				} `json:"system"`
			} `json:"exchanges"`
		} `json:"diff"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &res); err != nil {
		t.Fatal(err)
	}
	ex := res.Diff.Exchanges
	if !res.Diff.Changed || len(ex) != 1 || ex[0].Change != "changed" || len(ex[0].System) != 2 ||
		ex[0].System[0].Op != "-" || ex[0].System[0].Text != "You refund once." ||
		ex[0].System[1].Op != "+" || ex[0].System[1].Text != "You refund twice." {
		t.Fatalf("diff = %s", run.stdout)
	}
}

func TestCaptureRefresh_JSONNoChange_DiffNotChanged(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "same")

	run := refreshWith(t, bin, dir, "same", "--json")

	if err := validateOneJSONDocument(compileCLISchema(t, "cli/capture.schema.json"), []byte(run.stdout)); err != nil {
		t.Fatalf("stdout does not match schema: %v\n%s", err, run.stdout)
	}
	if !strings.Contains(run.stdout, `"changed": false`) || !strings.Contains(run.stdout, `"exchanges": []`) {
		t.Fatalf("stdout:\n%s", run.stdout)
	}
}

func TestCaptureRefresh_PlantedSecretInPrompt_NeverPrinted(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "Use key "+e2ePlantedKey+" for lookups.")

	human := refreshWith(t, bin, dir, "Use key "+e2ePlantedKey+" for refunds.")
	asJSON := refreshWith(t, bin, dir, "Use key "+e2ePlantedKey+" for audits.", "--json")

	for name, run := range map[string]replayRun{"human": human, "json": asJSON} {
		if run.code != 0 {
			t.Fatalf("%s: exit %d\n%s", name, run.code, run.stderr)
		}
		if strings.Contains(run.stdout, e2ePlantedKey) || strings.Contains(run.stderr, e2ePlantedKey) {
			t.Errorf("%s: the planted key was printed:\nstdout:\n%s\nstderr:\n%s", name, run.stdout, run.stderr)
		}
	}
	if !strings.Contains(human.stderr, "refunds.") || !strings.Contains(human.stderr, "lookups.") {
		t.Errorf("the changed words are missing, so the diff did not run:\n%s", human.stderr)
	}
}
