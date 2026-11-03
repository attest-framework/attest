package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	assetCLISchemaDir  = "../../../proto/attest/v1/cli"
	assetCLIFixtureDir = "../../../protocol-tests/cli"
	guidesSourceDir    = "../../../docs/guides"
)

// compileAssetCLISchema compiles proto/attest/v1/cli/<name>.schema.json.
func compileAssetCLISchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(filepath.Join(assetCLISchemaDir, name+".schema.json"))
	if err != nil {
		t.Fatalf("open schema: %v", err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(name+".schema.json", doc); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	s, err := c.Compile(name + ".schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return s
}

// validateAssetCLIJSON validates data against s.
func validateAssetCLIJSON(s *jsonschema.Schema, data string) error {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(data))
	if err != nil {
		return err
	}
	return s.Validate(doc)
}

// runAssetEngine runs bin with args and extra environment entries, returning
// stdout, stderr, and the exit code.
func runAssetEngine(t *testing.T, bin string, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("run engine: %v", err)
	}
	return stdout.String(), stderr.String(), 0
}

func TestAssetCLIFixtures_MatchSchemas(t *testing.T) {
	for _, name := range []string{"guide", "skills-list", "skills-install"} {
		s := compileAssetCLISchema(t, name)
		for _, kind := range []string{"valid", "invalid"} {
			paths, err := filepath.Glob(filepath.Join(assetCLIFixtureDir, name, kind, "*.json"))
			if err != nil || len(paths) == 0 {
				t.Fatalf("no %s %s fixtures (err=%v)", name, kind, err)
			}
			for _, p := range paths {
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				err = validateAssetCLIJSON(s, string(data))
				if kind == "valid" && err != nil {
					t.Errorf("%s/%s: want valid, got %v", name, filepath.Base(p), err)
				}
				if kind == "invalid" && err == nil {
					t.Errorf("%s/%s: want invalid, schema accepted it", name, filepath.Base(p))
				}
			}
		}
	}
}

func TestGuide_Topic_PrintsSourceMarkdownByteExactOffline(t *testing.T) {
	bin := buildEngineBinary(t)
	want, err := os.ReadFile(filepath.Join(guidesSourceDir, "capture.md"))
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAssetEngine(t, bin, []string{"HTTPS_PROXY=http://127.0.0.1:9", "HTTP_PROXY=http://127.0.0.1:9"}, "guide", "capture")

	if code != 0 || stdout != string(want) {
		t.Fatalf("exit %d, stdout matches docs/guides/capture.md: %v\nstderr: %s", code, stdout == string(want), stderr)
	}
}

func TestGuide_List_NamesEveryGuideWithTitle(t *testing.T) {
	bin := buildEngineBinary(t)
	sources, err := filepath.Glob(filepath.Join(guidesSourceDir, "*.md"))
	if err != nil || len(sources) == 0 {
		t.Fatalf("no guides (err=%v)", err)
	}

	stdout, _, code := runAssetEngine(t, bin, nil, "guide")

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != len(sources) {
		t.Fatalf("%d lines for %d guides:\n%s", len(lines), len(sources), stdout)
	}
	if !strings.Contains(stdout, "capture ") || !strings.Contains(stdout, "Capture: turn a run into a regression fixture") {
		t.Errorf("list lacks the capture topic and its title:\n%s", stdout)
	}
}

func TestGuide_JSON_MatchesSchema(t *testing.T) {
	bin := buildEngineBinary(t)
	s := compileAssetCLISchema(t, "guide")
	want, err := os.ReadFile(filepath.Join(guidesSourceDir, "policies.md"))
	if err != nil {
		t.Fatal(err)
	}

	list, _, listCode := runAssetEngine(t, bin, nil, "guide", "--json")
	one, _, oneCode := runAssetEngine(t, bin, nil, "guide", "policies", "--json")

	if listCode != 0 || oneCode != 0 {
		t.Fatalf("exit list=%d topic=%d, want 0", listCode, oneCode)
	}
	for _, out := range []string{list, one} {
		if err := validateAssetCLIJSON(s, out); err != nil {
			t.Errorf("output does not match guide schema: %v\n%s", err, out)
		}
	}
	var doc struct{ Topic, Title, Markdown string }
	if err := json.Unmarshal([]byte(one), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Topic != "policies" || doc.Title != "Policies" || doc.Markdown != string(want) {
		t.Errorf("topic=%q title=%q markdown matches source: %v", doc.Topic, doc.Title, doc.Markdown == string(want))
	}
}

func TestGuide_UnknownTopicOrExtraArgs_ExitsTwo(t *testing.T) {
	bin := buildEngineBinary(t)

	stdout, stderr, code := runAssetEngine(t, bin, nil, "guide", "no-such-topic")
	if code != 2 || stdout != "" {
		t.Fatalf("unknown topic: exit %d stdout %q, want 2 and empty", code, stdout)
	}
	if !strings.Contains(stderr, `unknown topic "no-such-topic"`) || !strings.Contains(stderr, "capture") {
		t.Errorf("stderr does not name the topic and list topics:\n%s", stderr)
	}
	for _, args := range [][]string{{"guide", "capture", "policies"}, {"guide", "--bogus"}} {
		if _, _, code := runAssetEngine(t, bin, nil, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
