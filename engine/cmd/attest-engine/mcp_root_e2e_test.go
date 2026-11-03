package main_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runMCPRoot starts `mcp` in dir with HOME set to home and returns the exit code
// and stderr after closing stdin, which ends a server that did start.
func runMCPRoot(t *testing.T, dir, home string, args ...string) (int, string) {
	t.Helper()
	bin := buildEngineBinary(t)
	cmd := exec.Command(bin, append([]string{"mcp"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, string(out)
}

func TestMCP_BroadWorkingDirectory_RefusedWithoutExplicitRoot(t *testing.T) {
	home := t.TempDir()
	if code, out := runMCPRoot(t, home, home); code != 2 || !strings.Contains(out, "home directory") {
		t.Errorf("home: exit %d, output %q", code, out)
	}
	if code, out := runMCPRoot(t, "/", home); code != 2 || !strings.Contains(out, "filesystem root") {
		t.Errorf("/: exit %d, output %q", code, out)
	}
}

func TestMCP_BroadWorkingDirectory_AllowedWithExplicitRoot(t *testing.T) {
	home := t.TempDir()
	if code, out := runMCPRoot(t, home, home, "--root", home); code != 0 {
		t.Errorf("explicit --root at home: exit %d, output %q", code, out)
	}
}

func TestMCP_ProjectWorkingDirectory_Serves(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	if code, out := runMCPRoot(t, project, home); code != 0 {
		t.Errorf("project dir: exit %d, output %q", code, out)
	}
}
