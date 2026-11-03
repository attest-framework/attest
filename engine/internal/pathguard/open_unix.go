//go:build unix

package pathguard

import "syscall"

// nonBlock keeps opening a named pipe from blocking.
const nonBlock = syscall.O_NONBLOCK
