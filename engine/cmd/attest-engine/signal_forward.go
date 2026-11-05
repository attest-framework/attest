package main

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// startForwardingSignals starts cmd and relays SIGINT and SIGTERM to it until
// stop is called. The handler is installed before the child starts: a signal
// that arrived between the start and the handler would kill the engine with
// the default action and orphan the child, which still holds the engine's
// stdout and stderr. A signal received before the start is relayed once the
// child runs.
func startForwardingSignals(cmd *exec.Cmd) (stop func(), err error) {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	if err := cmd.Start(); err != nil {
		signal.Stop(signals)
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-signals:
				_ = cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
	}, nil
}
