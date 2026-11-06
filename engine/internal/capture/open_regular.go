package capture

import (
	"fmt"
	"os"
)

// OpenRegular opens path for reading and returns the handle only when it is a
// regular file (a symlink to one is followed). The mode is read from the open
// handle, so the file cannot change type between the check and the read, and
// a FIFO or device cannot block the open. The error of a missing file
// satisfies errors.Is(err, fs.ErrNotExist).
func OpenRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|openFlags, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("not a regular file (%s)", info.Mode().Type())
	}
	return f, nil
}
