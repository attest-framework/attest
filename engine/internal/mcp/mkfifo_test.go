//go:build unix

package mcp_test

import "syscall"

func mkfifo(path string) error { return syscall.Mkfifo(path, 0o644) }
