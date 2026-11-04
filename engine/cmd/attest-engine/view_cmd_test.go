package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	viewTestHead   = "../../internal/report/testdata/view/head.json"
	viewTestBase   = "../../internal/report/testdata/view/base.json"
	viewTestMutate = "../../internal/report/testdata/view/mutate.json"
)

// openRecorder is an injected opener that records the paths it was asked to open.
type openRecorder struct {
	paths []string
	err   error
}

func (o *openRecorder) open(path string) error {
	o.paths = append(o.paths, path)
	return o.err
}

func runViewForTest(t *testing.T, env viewEnv, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runView(args, &out, &errb, env)
	return code, out.String(), errb.String()
}

func TestRunView_OpensOnlyOnTTYOutsideCI(t *testing.T) {
	cases := []struct {
		name     string
		env      viewEnv
		extra    []string
		wantOpen bool
	}{
		{"tty, no CI", viewEnv{isTTY: true}, nil, true},
		{"not a tty", viewEnv{isTTY: false}, nil, false},
		{"CI set", viewEnv{isTTY: true, ci: true}, nil, false},
		{"--no-open", viewEnv{isTTY: true}, []string{"--no-open"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &openRecorder{}
			tc.env.open = rec.open
			out := filepath.Join(t.TempDir(), "page.html")
			args := append([]string{viewTestHead, "--out", out}, tc.extra...)

			code, _, stderr := runViewForTest(t, tc.env, args...)

			if code != exitOK {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if got := len(rec.paths) == 1; got != tc.wantOpen {
				t.Fatalf("opener calls = %v, want opened=%v", rec.paths, tc.wantOpen)
			}
			if tc.wantOpen && (rec.paths[0] != out || !filepath.IsAbs(rec.paths[0])) {
				t.Errorf("opened %q, want the absolute page path %q", rec.paths[0], out)
			}
		})
	}
}

func TestRunView_OpenerFailure_PrintsPathAndExitsZero(t *testing.T) {
	rec := &openRecorder{err: errors.New("no display")}
	out := filepath.Join(t.TempDir(), "page.html")

	code, _, stderr := runViewForTest(t, viewEnv{isTTY: true, open: rec.open}, viewTestHead, "--out", out)

	if code != exitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	if !strings.Contains(stderr, out) || !strings.Contains(stderr, "no display") {
		t.Errorf("stderr must name the path and the error: %q", stderr)
	}
}

func TestRunView_JSON_ReportsCountsAndOpened(t *testing.T) {
	rec := &openRecorder{}
	out := filepath.Join(t.TempDir(), "page.html")

	code, stdout, stderr := runViewForTest(t, viewEnv{isTTY: true, open: rec.open},
		viewTestHead, "--baseline", viewTestBase, "--mutation", viewTestMutate, "--out", out, "--json")

	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var got viewResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	want := viewResult{Out: out, Reports: 1, Tests: 3, Failures: 1, Traces: 2, Baseline: true, Mutation: true, Opened: true}
	if got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

func TestRunView_DefaultOutIsNextToFirstReport_Mode0644_NoTempLeft(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "attest-report.json")
	data, err := os.ReadFile(viewTestHead)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, data, 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runViewForTest(t, viewEnv{}, report)

	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	page := filepath.Join(dir, "attest-report.html")
	info, err := os.Stat(page)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("want the report and the page only, got %d entries", len(entries))
	}
}

func TestRunView_FlagsAndReportsInAnyOrder(t *testing.T) {
	out := filepath.Join(t.TempDir(), "p.html")

	code, stdout, stderr := runViewForTest(t, viewEnv{}, "--out", out, viewTestHead, "--no-open", viewTestHead, "--json")

	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, `"reports":2`) {
		t.Errorf("both reports must merge: %s", stdout)
	}
}

func TestRunView_UsageAndInputErrors_ExitTwoWithNothingOnStdout(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"no report":        {},
		"unknown flag":     {viewTestHead, "--bogus"},
		"missing report":   {filepath.Join(dir, "none.json")},
		"malformed":        {bad},
		"out is an input":  {viewTestHead, "--out", viewTestHead},
		"missing baseline": {viewTestHead, "--baseline", filepath.Join(dir, "none.json")},
		"unwritable out":   {viewTestHead, "--out", filepath.Join(dir, "no-such-dir", "p.html")},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			rec := &openRecorder{}

			code, stdout, _ := runViewForTest(t, viewEnv{isTTY: true, open: rec.open}, append(args, "--json")...)

			if code != exitUsage {
				t.Errorf("exit %d, want 2", code)
			}
			if stdout != "" {
				t.Errorf("stdout must be empty on error, got %q", stdout)
			}
			if len(rec.paths) != 0 {
				t.Error("must not open a browser on error")
			}
		})
	}
}
