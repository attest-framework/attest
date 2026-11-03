//go:build !unix

package mcp_test

import "errors"

func mkfifo(string) error { return errors.New("named pipes are unavailable on this platform") }
