package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func selectRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"commit", "-q", "-m", "init"},
		{"checkout", "-q", "-b", "feature"},
	}
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "test_a.py"), []byte("def test_a(): pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range steps {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestRunSelect_TouchedTest_PrintsOnePathPerLine(t *testing.T) {
	dir := selectRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "tests", "test_a.py"), []byte("def test_a(): assert 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runSelect([]string{"--base", "main", "--repo", dir}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if out.String() != "tests/test_a.py\n" {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestRunSelect_LockfileChange_PrintsAll(t *testing.T) {
	dir := selectRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "uv.lock"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runSelect([]string{"--base", "main", "--repo", dir}, &out, &errOut); code != 0 || out.String() != "all\n" {
		t.Errorf("code %d stdout %q", code, out.String())
	}
}

func TestRunSelect_NoChanges_PrintsNothing(t *testing.T) {
	dir := selectRepo(t)
	var out, errOut bytes.Buffer
	if code := runSelect([]string{"--base", "main", "--repo", dir}, &out, &errOut); code != 0 || out.Len() != 0 {
		t.Errorf("code %d stdout %q", code, out.String())
	}
}

func TestRunSelect_JSON_HasModeTestsReasons(t *testing.T) {
	dir := selectRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runSelect([]string{"--base", "main", "--repo", dir, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var got struct {
		Mode    string
		Tests   []string
		Reasons []struct{ Path, Class string }
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "subset" || len(got.Tests) != 0 || len(got.Reasons) != 1 || got.Reasons[0].Class != "ignored" {
		t.Errorf("got %+v", got)
	}
	if !strings.Contains(out.String(), `"tests":[]`) {
		t.Errorf("tests must encode as [], got %s", out.String())
	}
}

func TestRunSelect_BadRef_Exit2(t *testing.T) {
	dir := selectRepo(t)
	var out, errOut bytes.Buffer
	if code := runSelect([]string{"--base", "nope", "--repo", dir}, &out, &errOut); code != 2 || errOut.Len() == 0 {
		t.Errorf("code %d stderr %q", code, errOut.String())
	}
}

func TestRunSelect_MissingBase_Exit2(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runSelect(nil, &out, &errOut); code != 2 {
		t.Errorf("code %d, want 2", code)
	}
}
