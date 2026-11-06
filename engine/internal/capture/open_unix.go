//go:build unix

package capture

import "syscall"

// openFlags keeps opening a FIFO from blocking until a writer appears; the
// caller rejects it after the open, by the handle's mode.
const openFlags = syscall.O_NONBLOCK
