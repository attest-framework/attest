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

	"github.com/attest-framework/attest/engine/internal/llm/providers/acptest"
)

// completeHangingAgent runs p against the Hang stub, whose agent starts a
// child holding stdout and stderr (as npx does) and never answers. It
// asserts the call fails within bound and the child is killed.
func completeHangingAgent(t *testing.T, ctx context.Context, agentTimeout, bound time.Duration) {
	t.Helper()
	clearCI(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	p, err := NewACP(acptest.Command(acptest.Hang, pidFile), "", agentTimeout)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = p.Complete(ctx, acpJudgeRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Complete succeeded against an agent that never answers")
	}
	if elapsed > bound+acpShutdownGrace+2*time.Second {
		t.Errorf("Complete returned after %v; the deadline was %v", elapsed, bound)
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
			t.Fatalf("agent child %d survived the deadline", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestACP_Complete_CallerDeadline_KillsAgentAndDescendants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	completeHangingAgent(t, ctx, testACPTimeout, time.Second)
}

// TestACP_Complete_NoCallerDeadline_OwnTimeoutKillsAgent covers callers such
// as generate_user_message that pass context.Background().
func TestACP_Complete_NoCallerDeadline_OwnTimeoutKillsAgent(t *testing.T) {
	completeHangingAgent(t, context.Background(), time.Second, time.Second)
}

func TestNewACP_RelativeCommand_ResolvedToAbsolutePath(t *testing.T) {
	clearCI(t)
	dir := t.TempDir()
	if err := os.Symlink(os.Args[0], filepath.Join(dir, "agent")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	p, err := NewACP("./agent", "", testACPTimeout)

	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.argv[0], "/") {
		t.Errorf("argv[0] = %q, want an absolute path", p.argv[0])
	}
}
