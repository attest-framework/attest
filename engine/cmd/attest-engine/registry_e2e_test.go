package main_test

import (
	"strings"
	"testing"
)

var registeredCommands = []string{
	"baseline", "cache", "calibrate", "capabilities", "capture", "doctor", "guide", "mcp", "mutate",
	"policy", "replay", "report", "select", "skills", "trace", "version", "view", "world",
}

func TestHelp_ListsEveryCommand_ExitsZero(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, flag := range []string{"--help", "-h", "help"} {
		stdout, _, code := runEngine(t, bin, t.TempDir(), flag)
		if code != 0 {
			t.Fatalf("%s: exit %d, want 0", flag, code)
		}
		for _, name := range registeredCommands {
			if !strings.Contains(stdout, "  "+name+" ") {
				t.Errorf("%s output missing command %q:\n%s", flag, name, stdout)
			}
		}
	}
}

func TestUnknownCommand_ExitsTwoWithUsage(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, arg := range []string{"frobnicate", ""} {
		stdout, stderr, code := runEngine(t, bin, t.TempDir(), arg)

		if code != 2 {
			t.Fatalf("%q: exit %d, want 2\n%s", arg, code, stderr)
		}
		if stdout != "" {
			t.Errorf("%q: stdout = %q, want empty", arg, stdout)
		}
		if !strings.Contains(stderr, "unknown command: "+arg) || !strings.Contains(stderr, "commands:") {
			t.Errorf("%q: stderr lacks error and usage:\n%s", arg, stderr)
		}
	}
}

func TestVersion_PrintsEngineVersion(t *testing.T) {
	bin := buildEngineBinary(t)

	stdout, _, code := runEngine(t, bin, t.TempDir(), "version")

	if code != 0 || !strings.HasPrefix(stdout, "attest-engine ") {
		t.Fatalf("exit %d stdout %q, want 0 and attest-engine <version>", code, stdout)
	}
}

func TestCache_NoSubcommand_ExitsTwo(t *testing.T) {
	bin := buildEngineBinary(t)

	_, stderr, code := runEngine(t, bin, t.TempDir(), "cache")

	if code != 2 || !strings.Contains(stderr, "usage: attest-engine cache") {
		t.Fatalf("exit %d stderr %q, want 2 and cache usage", code, stderr)
	}
}
