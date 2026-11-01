package main_test

import (
	"strings"
	"testing"
)

var registeredCommands = []string{"baseline", "cache", "calibrate", "policy", "version"}

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

	stdout, stderr, code := runEngine(t, bin, t.TempDir(), "frobnicate")

	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "unknown command: frobnicate") || !strings.Contains(stderr, "commands:") {
		t.Errorf("stderr lacks error and usage:\n%s", stderr)
	}
}

func TestVersion_PrintsEngineVersion(t *testing.T) {
	bin := buildEngineBinary(t)

	stdout, _, code := runEngine(t, bin, t.TempDir(), "version")

	if code != 0 || !strings.HasPrefix(stdout, "attest-engine ") {
		t.Fatalf("exit %d stdout %q, want 0 and attest-engine <version>", code, stdout)
	}
}

func TestCache_NoSubcommand_ExitsOne(t *testing.T) {
	bin := buildEngineBinary(t)

	_, stderr, code := runEngine(t, bin, t.TempDir(), "cache")

	if code != 1 || !strings.Contains(stderr, "usage: attest-engine cache") {
		t.Fatalf("exit %d stderr %q, want 1 and cache usage", code, stderr)
	}
}
