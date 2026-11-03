//go:build unix

package pathguard

import (
	"os"
	"syscall"
)

// openRegular opens path for reading without following a final symlink and
// without blocking on a named pipe.
func openRegular(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
