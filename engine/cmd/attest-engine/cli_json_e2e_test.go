package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// cliJSONRunner runs one command or subcommand with --json against fixtures
// it creates under tmp, checks its exit code, and returns its stdout. env is
// engineEnv(tmp/cache): no provider variables, no CI markers.
type cliJSONRunner func(t *testing.T, bin string, env []string, tmp string) []byte

// cliJSONRunners has one entry per registered command without subcommands,
// keyed by the command name ("version"), and one per subcommand, keyed by
// "<command> <subcommand>" ("cache stats"). TestCLIRegistry_Coverage fails
// when a registered command or subcommand has no entry.
var cliJSONRunners = map[string]cliJSONRunner{
	"version": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "version", "--json")
	},
	"guide": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "guide", "capture", "--json")
	},
	"skills list": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "skills", "list", "--json")
	},
	"skills install": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "skills", "install", "--dir", filepath.Join(tmp, "project"), "--json")
	},
	"doctor": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "doctor", "--json")
	},
	"cache stats": func(t *testing.T, bin string, env []string, tmp string) []byte {
		cliWriteFile(t, filepath.Join(tmp, "cache", "entry.bin"), "cached")
		return cliRun(t, bin, env, tmp, 0, "cache", "stats", "--json")
	},
	"cache clear": func(t *testing.T, bin string, env []string, tmp string) []byte {
		cliWriteFile(t, filepath.Join(tmp, "cache", "entry.bin"), "cached")
		return cliRun(t, bin, env, tmp, 0, "cache", "clear", "--json")
	},
	"baseline pin": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "baseline", "pin", "--json", "--tag", "v1", "--report", cliReportV2(t, tmp))
	},
	"baseline list": func(t *testing.T, bin string, env []string, tmp string) []byte {
		cliRun(t, bin, env, tmp, 0, "baseline", "pin", "--tag", "v1", "--report", cliReportV2(t, tmp))
		return cliRun(t, bin, env, tmp, 0, "baseline", "list", "--json")
	},
	"baseline show": func(t *testing.T, bin string, env []string, tmp string) []byte {
		cliRun(t, bin, env, tmp, 0, "baseline", "pin", "--tag", "v1", "--report", cliReportV2(t, tmp))
		return cliRun(t, bin, env, tmp, 0, "baseline", "show", "--json", "--tag", "v1")
	},
	"baseline delete": func(t *testing.T, bin string, env []string, tmp string) []byte {
		cliRun(t, bin, env, tmp, 0, "baseline", "pin", "--tag", "v1", "--report", cliReportV2(t, tmp))
		return cliRun(t, bin, env, tmp, 0, "baseline", "delete", "--json", "--tag", "v1")
	},
	"calibrate": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "calibrate", "--json", "--labels", writeLabels(t))
	},
	"capture": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "capture", "--json", "--name", "inc", "--from", cliAbs(t, inc1042OTLP), "--out-dir", filepath.Join(tmp, "fixtures"))
	},
	"mutate": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "mutate", "--json", cliAbs(t, mutateFixture))
	},
	"policy evaluate": func(t *testing.T, bin string, env []string, tmp string) []byte {
		policyPath := filepath.Join(tmp, "policy.yaml")
		cliWriteFile(t, policyPath, "layers:\n  1:\n    max_hard_fails: 0\n")
		return cliRun(t, bin, env, tmp, 1, "policy", "evaluate", "--json", "--policy", policyPath, "--report", cliReportV2(t, tmp))
	},
	"replay": func(t *testing.T, bin string, env []string, tmp string) []byte {
		// The command prints to stdout and exits 3: under --json its stdout
		// goes to stderr, the result carries exit_code 3 and replay exits 1.
		out := cliRun(t, bin, env, tmp, 1, "replay", "--json", "--fixture", cliAbs(t, cliReplayFixture), "--", "sh", "-c", "echo agent-stdout; exit 3")
		var res struct {
			ExitCode *int `json:"exit_code"`
		}
		if err := json.Unmarshal(out, &res); err != nil || res.ExitCode == nil || *res.ExitCode != 3 {
			t.Errorf("replay --json exit_code: %s (err=%v), want 3", out, err)
		}
		return out
	},
	"report junit": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "report", "junit", "--json", "--report", cliAbs(t, mixedCIReport), "--out", filepath.Join(tmp, "junit.xml"))
	},
	"report summary": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "report", "summary", "--json", "--report", cliAbs(t, mixedCIReport))
	},
	"report rows": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "report", "rows", "--json", "--report", cliAbs(t, mixedCIReport))
	},
	"select": func(t *testing.T, bin string, env []string, tmp string) []byte {
		repo := cliGitRepo(t, filepath.Join(tmp, "repo"))
		return cliRun(t, bin, env, tmp, 0, "select", "--json", "--base", "HEAD~1", "--repo", repo)
	},
	"trace check": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "trace", "check", "--json", cliAbs(t, inc1042OTLP))
	},
	"trace convert": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "trace", "convert", "--json", cliAbs(t, inc1042OTLP))
	},
	"world": func(t *testing.T, bin string, env []string, tmp string) []byte {
		return cliRun(t, bin, env, tmp, 0, "world", "--json", "--file", cliAbs(t, worldYAML), "--", "sh", "-c", "echo agent-stdout")
	},
}

const (
	mixedCIReport    = "../../internal/report/testdata/ci/mixed.json"
	cliReplayFixture = "../../../protocol-tests/fixture-v1/valid/001-command-capture-expect-fail.attest.json"
)

// cliRun runs bin with args in dir, fails the test unless it exits wantCode,
// and returns its stdout.
func cliRun(t *testing.T, bin string, env []string, dir string, wantCode int, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	if code != wantCode {
		t.Fatalf("%v: exit %d, want %d\nstdout: %s\nstderr: %s", args, code, wantCode, stdout.String(), stderr.String())
	}
	return stdout.Bytes()
}

func cliAbs(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func cliWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// cliReportV2 writes a report-v2 file with two passing and one failing
// assertion and returns its path.
func cliReportV2(t *testing.T, tmp string) string {
	t.Helper()
	path := filepath.Join(tmp, "report.json")
	cliWriteFile(t, path, minimalReportV2(2, 1))
	return path
}

// cliGitRepo creates a repository with two commits, the second touching a
// test file, and returns its path.
func cliGitRepo(t *testing.T, dir string) string {
	t.Helper()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.org",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.org")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cliWriteFile(t, filepath.Join(dir, "README.md"), "repo\n")
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	cliWriteFile(t, filepath.Join(dir, "tests", "test_refund.py"), "def test_refund():\n    pass\n")
	git("add", ".")
	git("commit", "-q", "-m", "add test")
	return dir
}

// cliHelpEntry is one command of `help --json`.
type cliHelpEntry struct {
	Name        string `json:"name"`
	Schema      string `json:"schema"`
	Subcommands []struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
	} `json:"subcommands"`
	ExitCodes []struct {
		Code    int    `json:"code"`
		Meaning string `json:"meaning"`
	} `json:"exit_codes"`
}

// cliTarget is one runnable unit of the registry: a command without
// subcommands or one subcommand, with the schema of its --json output.
type cliTarget struct {
	key, schema string
}

// cliRegistry runs `help --json` on bin, validates it against
// cli/help.schema.json and returns the commands it lists.
func cliRegistry(t *testing.T, bin string) []cliHelpEntry {
	t.Helper()
	tmp := t.TempDir()
	out := cliRun(t, bin, engineEnv(filepath.Join(tmp, "cache")), tmp, 0, "help", "--json")
	if err := validateOneJSONDocument(compileCLISchema(t, "cli/help.schema.json"), out); err != nil {
		t.Fatalf("help --json does not match cli/help.schema.json: %v\n%s", err, out)
	}
	var doc struct {
		Commands []cliHelpEntry `json:"commands"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Commands
}

// cliTargets flattens the registry into runner keys and schemas.
func cliTargets(cmds []cliHelpEntry) []cliTarget {
	var out []cliTarget
	for _, c := range cmds {
		if len(c.Subcommands) == 0 {
			out = append(out, cliTarget{c.Name, c.Schema})
			continue
		}
		for _, s := range c.Subcommands {
			out = append(out, cliTarget{c.Name + " " + s.Name, s.Schema})
		}
	}
	return out
}

// TestCLIRegistry_Coverage fails when a registered command or subcommand
// lacks a --json schema, a schema file on disk, a runner in cliJSONRunners
// or exit-code declarations, and when a runner names nothing registered.
func TestCLIRegistry_Coverage(t *testing.T) {
	bin := buildEngineBinary(t)
	cmds := cliRegistry(t, bin)

	registered := map[string]bool{}
	for _, c := range cmds {
		if len(c.ExitCodes) == 0 {
			t.Errorf("%s: no exit codes declared", c.Name)
		}
		if len(c.Subcommands) == 0 && c.Schema == "" {
			t.Errorf("%s: no --json schema declared", c.Name)
		}
	}
	for _, target := range cliTargets(cmds) {
		registered[target.key] = true
		if target.schema == "" {
			t.Errorf("%s: no --json schema declared", target.key)
		} else if _, err := os.Stat(filepath.Join(cliProtoRoot, filepath.FromSlash(target.schema))); err != nil {
			t.Errorf("%s: schema %s: %v", target.key, target.schema, err)
		}
		if _, ok := cliJSONRunners[target.key]; !ok {
			t.Errorf("%s: no entry in cliJSONRunners", target.key)
		}
	}
	var stale []string
	for key := range cliJSONRunners {
		if !registered[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("cliJSONRunners entries for unregistered commands: %s", strings.Join(stale, ", "))
	}
}

// TestCLIJSON_EveryCommandMatchesItsSchema runs every registered command and
// subcommand with --json and validates its real stdout, which must be exactly
// one JSON document, against the schema the registry declares for it.
func TestCLIJSON_EveryCommandMatchesItsSchema(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, target := range cliTargets(cliRegistry(t, bin)) {
		t.Run(target.key, func(t *testing.T) {
			run, ok := cliJSONRunners[target.key]
			if !ok || target.schema == "" {
				t.Skip("reported by TestCLIRegistry_Coverage")
			}
			tmp := t.TempDir()
			out := run(t, bin, engineEnv(filepath.Join(tmp, "cache")), tmp)
			if err := validateOneJSONDocument(compileCLISchema(t, target.schema), out); err != nil {
				t.Fatalf("stdout does not match %s: %v\n%s", target.schema, err, out)
			}
		})
	}
}

// TestCLIJSON_ServeRefusesJSON checks that --json with --serve, which writes
// JSON lines of its own, is a usage error with nothing on stdout.
func TestCLIJSON_ServeRefusesJSON(t *testing.T) {
	bin := buildEngineBinary(t)
	cases := map[string][]string{
		"capture": {"capture", "--json", "--name", "x", "--serve"},
		"replay":  {"replay", "--json", "--fixture", cliAbs(t, cliReplayFixture), "--serve"},
		"world":   {"world", "--json", "--file", cliAbs(t, worldYAML), "--serve"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			if out := cliRun(t, bin, engineEnv(filepath.Join(tmp, "cache")), tmp, 2, args...); len(out) != 0 {
				t.Errorf("stdout %q, want empty", out)
			}
		})
	}
}

// TestCaptureJSON_CommandStdoutGoesToStderr captures a scripted agent with
// --json: stdout is the one result object, with the agent's exit code, and
// the agent's own stdout is on stderr.
func TestCaptureJSON_CommandStdoutGoesToStderr(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	// -test.v makes the helper agent print "=== RUN" to its stdout.
	args := []string{"capture", "--json", "--expect-fail", "--name", "scripted", "--out-dir", filepath.Join(dir, "fixtures"),
		"--", os.Args[0], "-test.v", "-test.run=^TestCaptureHelperAgent$"}
	env := []string{"ATTEST_CAPTURE_HELPER=1", "OPENAI_BASE_URL=" + fakeModel(t), "OPENAI_API_KEY=" + e2ePlantedKey}
	run := engineRun(t, bin, dir, env, args...)
	if run.code != 0 {
		t.Fatalf("exit %d\n%s", run.code, run.stderr)
	}
	if err := validateOneJSONDocument(compileCLISchema(t, "cli/capture.schema.json"), []byte(run.stdout)); err != nil {
		t.Fatalf("stdout does not match cli/capture.schema.json: %v\n%s", err, run.stdout)
	}
	var res struct {
		ExitCode *int   `json:"exit_code"`
		Source   string `json:"source"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &res); err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 || res.Source != "command" || !res.Verified {
		t.Errorf("result %s, want exit_code 0, source command, verified", run.stdout)
	}
	if !strings.Contains(run.stderr, "=== RUN") {
		t.Errorf("the agent's stdout is not on stderr:\n%s", run.stderr)
	}
}

// TestCaptureRefresh_CIOrJSON_RefusesWithoutYes checks the refresh
// confirmation never prompts under CI or --json: it refuses with exit 2, a
// --yes hint, and runs nothing.
func TestCaptureRefresh_CIOrJSON_RefusesWithoutYes(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	captureWithSystem(t, bin, dir, "s")
	path := filepath.Join(dir, "fixtures", "scripted.attest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "ran")
	doc["source"].(map[string]any)["command"] = []any{"touch", marker}
	data, _ = json.Marshal(doc)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"capture", "--refresh", "scripted", "--out-dir", filepath.Join(dir, "fixtures")}
	cases := map[string]struct {
		env  []string
		args []string
	}{
		"CI set": {append(replayEnv("s"), "CI=true"), args},
		"--json": {replayEnv("s"), append(append([]string{}, args...), "--json")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			run := engineRun(t, bin, dir, c.env, c.args...)
			if run.code != 2 || run.stdout != "" || !strings.Contains(run.stderr, "re-run with --yes") {
				t.Fatalf("exit %d stdout %q stderr:\n%s", run.code, run.stdout, run.stderr)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("the command ran without confirmation")
			}
		})
	}
}
