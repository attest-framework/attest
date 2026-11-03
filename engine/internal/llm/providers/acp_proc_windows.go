//go:build windows

package providers

import (
	"os/exec"
	"strconv"
)

// startInProcessGroup makes context cancellation kill the agent's whole
// process tree (npx.cmd starts node as a child of cmd.exe).
func startInProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return killTree(cmd) }
}

func killTree(cmd *exec.Cmd) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
