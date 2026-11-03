//go:build !unix

package pathguard

import "os"

func openRegular(path string) (*os.File, error) { return os.Open(path) }
