//go:build !windows

package providers

import (
	"os/exec"
	"syscall"
)

// startInProcessGroup starts the agent as the leader of a new process group
// and makes context cancellation kill the whole group.
func startInProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
