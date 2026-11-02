//go:build !windows

package providers

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/attest-ai/attest/engine/internal/llm/providers/acptest"
)

// TestACP_Complete_TimeoutKillsAgentAndDescendants runs an agent that starts
// a child holding stdout and stderr (as npx does) and never answers: the
// judge deadline must end the call promptly and kill the child too.
func TestACP_Complete_TimeoutKillsAgentAndDescendants(t *testing.T) {
	p := newStubACP(t, acptest.Hang)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv(acptest.PIDFileEnv, pidFile)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	_, err := p.Complete(ctx, acpJudgeRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Complete succeeded against an agent that never answers")
	}
	if elapsed > time.Second+acpShutdownGrace+2*time.Second {
		t.Errorf("Complete returned after %v; the deadline was 1s", elapsed)
	}
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("stub never started its child: %v", readErr)
	}
	pid, _ := strconv.Atoi(string(raw))
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("agent child %d survived the judge deadline", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestNewACP_RelativeCommand_ResolvedToAbsolutePath(t *testing.T) {
	clearCI(t)
	dir := t.TempDir()
	if err := os.Symlink(os.Args[0], filepath.Join(dir, "agent")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	p, err := NewACP("./agent", "")

	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.argv[0], "/") {
		t.Errorf("argv[0] = %q, want an absolute path", p.argv[0])
	}
}
